package events

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	runnersv1alpha1 "github.com/mohamedhabas11/runner_operator/api/v1alpha1"
)

const (
	maxPayloadSize  = 1 << 20 // 1 MB
	shutdownTimeout = 5 * time.Second
)

// RouteConfig holds the configuration for a registered webhook route.
type RouteConfig struct {
	Trigger     runnersv1alpha1.EventTrigger
	SecretValue []byte
}

// Server is an HTTP server that receives external webhooks and creates Workflow CRs.
type Server struct {
	client client.Client
	scheme *runtime.Scheme

	mu       sync.RWMutex
	routes   map[string]*RouteConfig
	rateData map[string]*rateCounter

	httpServer *http.Server
	port       string
}

type rateCounter struct {
	mu     sync.Mutex
	counts []time.Time
}

func newRateCounter() *rateCounter {
	return &rateCounter{}
}

func (rc *rateCounter) allow(maxPerMinute int) bool {
	if maxPerMinute <= 0 {
		return true
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-1 * time.Minute)

	j := 0
	for i, t := range rc.counts {
		if t.After(cutoff) {
			rc.counts[j] = rc.counts[i]
			j++
		}
	}
	rc.counts = rc.counts[:j]

	if len(rc.counts) >= maxPerMinute {
		return false
	}
	rc.counts = append(rc.counts, now)
	return true
}

// NewServer creates a new webhook event server.
func NewServer(cl client.Client, scheme *runtime.Scheme, port string) *Server {
	return &Server{
		client:   cl,
		scheme:   scheme,
		routes:   make(map[string]*RouteConfig),
		rateData: make(map[string]*rateCounter),
		port:     port,
	}
}

// RegisterRoute adds or updates a webhook route from an EventTrigger.
func (s *Server) RegisterRoute(ctx context.Context, trigger runnersv1alpha1.EventTrigger) error {
	if trigger.Spec.Webhook == nil || trigger.Spec.Webhook.Path == "" {
		return fmt.Errorf("EventTrigger %s/%s has no webhook path", trigger.Namespace, trigger.Name)
	}

	var secretValue []byte
	if trigger.Spec.Webhook.SecretRef != nil {
		secret := &corev1.Secret{}
		ns := trigger.Spec.Webhook.SecretRef.Namespace
		if ns == "" {
			ns = trigger.Namespace
		}
		if err := s.client.Get(ctx, types.NamespacedName{
			Name:      trigger.Spec.Webhook.SecretRef.Name,
			Namespace: ns,
		}, secret); err != nil {
			return fmt.Errorf("failed to fetch webhook secret %s/%s: %w", ns, trigger.Spec.Webhook.SecretRef.Name, err)
		}
		secretValue = secret.Data["hmac-secret"]
		if len(secretValue) == 0 {
			secretValue = secret.Data["token"]
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.routes[trigger.Spec.Webhook.Path] = &RouteConfig{
		Trigger:     trigger,
		SecretValue: secretValue,
	}
	return nil
}

// DeregisterRoute removes a webhook route.
func (s *Server) DeregisterRoute(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.routes, path)
	delete(s.rateData, path)
}

// Start begins listening for webhook requests.  Blocks until ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRequest)

	s.httpServer = &http.Server{
		Addr:         s.port,
		Handler:      withLogging(mux),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.FromContext(ctx).Info("Webhook event server starting", "port", s.port)
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return s.httpServer.Shutdown(shutdownCtx)
	}
}

func (s *Server) handleRequest(w http.ResponseWriter, r *http.Request) {
	logger := log.FromContext(r.Context())

	s.mu.RLock()
	route, ok := s.routes[r.URL.Path]
	s.mu.RUnlock()

	if !ok {
		logger.Info("No route registered for path", "path", r.URL.Path)
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	trigger := route.Trigger

	// IP restriction
	if len(trigger.Spec.Webhook.AllowedIPs) > 0 {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		remoteIP := net.ParseIP(host)
		if remoteIP == nil {
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}
		allowed := false
		for _, cidr := range trigger.Spec.Webhook.AllowedIPs {
			_, cidrNet, err := net.ParseCIDR(cidr)
			if err != nil {
				continue
			}
			if cidrNet.Contains(remoteIP) {
				allowed = true
				break
			}
		}
		if !allowed {
			logger.Info("Request from disallowed IP", "ip", remoteIP, "path", r.URL.Path)
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
	}

	// Rate limit
	if trigger.Spec.RateLimit != nil && trigger.Spec.RateLimit.MaxPerMinute > 0 {
		s.mu.Lock()
		rc, exists := s.rateData[trigger.Spec.Webhook.Path]
		if !exists {
			rc = newRateCounter()
			s.rateData[trigger.Spec.Webhook.Path] = rc
		}
		s.mu.Unlock()

		if !rc.allow(trigger.Spec.RateLimit.MaxPerMinute) {
			logger.Info("Rate limit exceeded", "path", r.URL.Path)
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}
	}

	// Read body
	body, err := readBody(r)
	if err != nil {
		logger.Error(err, "Failed to read request body")
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	// HMAC validation
	if len(route.SecretValue) > 0 {
		sig := r.Header.Get("X-Hub-Signature-256")
		if sig == "" {
			sig = r.Header.Get("X-Hub-Signature")
			if sig != "" {
				// Convert sha1=... to SHA256 for backward compat
				if !validateHMAC(route.SecretValue, body, sig, "sha1=") {
					logger.Info("HMAC validation failed (SHA1)")
					http.Error(w, "Unauthorized", http.StatusUnauthorized)
					return
				}
			} else {
				logger.Info("Missing HMAC signature header")
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		} else {
			if !validateHMAC(route.SecretValue, body, sig, "sha256=") {
				logger.Info("HMAC validation failed (SHA256)")
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		}
	}

	// Parse payload
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		logger.Error(err, "Failed to parse JSON payload")
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	// Extract parameters
	params, err := extractParams(trigger.Spec.Parameters, payload)
	if err != nil {
		logger.Error(err, "Failed to extract required parameters from payload")
		http.Error(w, "Bad Request: missing required parameter", http.StatusBadRequest)
		return
	}

	// Create Workflow
	if err := s.createWorkflow(r.Context(), trigger, params); err != nil {
		logger.Error(err, "Failed to create Workflow from trigger", "trigger", trigger.Name)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusAccepted)
	_, _ = fmt.Fprint(w, "Accepted")
}

func (s *Server) createWorkflow(ctx context.Context, trigger runnersv1alpha1.EventTrigger, params map[string]string) error {
	if len(trigger.Spec.AllowedNamespaces) > 0 {
		allowed := slices.Contains(trigger.Spec.AllowedNamespaces, trigger.Namespace)
		if !allowed {
			return fmt.Errorf("trigger namespace %s not in allowed namespaces", trigger.Namespace)
		}
	}

	ns := trigger.Namespace
	if trigger.Spec.WorkflowTemplate.Namespace != "" {
		ns = trigger.Spec.WorkflowTemplate.Namespace
	}

	template := &runnersv1alpha1.Workflow{}
	if err := s.client.Get(ctx, types.NamespacedName{
		Name:      trigger.Spec.WorkflowTemplate.Name,
		Namespace: ns,
	}, template); err != nil {
		return fmt.Errorf("failed to fetch workflow template: %w", err)
	}

	workflow := template.DeepCopy()
	workflow.ObjectMeta = metav1.ObjectMeta{
		GenerateName: template.Name + "-",
		Namespace:    trigger.Namespace,
		Labels: map[string]string{
			"app.kubernetes.io/name":           "runner-operator",
			"runner-operator.io/event-trigger": trigger.Name,
		},
	}

	// Inject parameters as env vars on the first step
	if len(params) > 0 && len(workflow.Spec.Steps) > 0 {
		for k, v := range params {
			workflow.Spec.Steps[0].Env = append(workflow.Spec.Steps[0].Env, corev1.EnvVar{
				Name:  k,
				Value: v,
			})
		}
	}
	if len(params) > 0 && len(workflow.Spec.Jobs) > 0 && len(workflow.Spec.Jobs[0].Steps) > 0 {
		for k, v := range params {
			workflow.Spec.Jobs[0].Steps[0].Env = append(workflow.Spec.Jobs[0].Steps[0].Env, corev1.EnvVar{
				Name:  k,
				Value: v,
			})
		}
	}

	// Set owner reference so workflow is garbage collected when trigger is deleted
	if err := controllerutil.SetControllerReference(&trigger, workflow, s.scheme); err != nil {
		return fmt.Errorf("failed to set owner reference: %w", err)
	}

	if err := s.client.Create(ctx, workflow); err != nil {
		return fmt.Errorf("failed to create workflow: %w", err)
	}

	return nil
}

func readBody(r *http.Request) ([]byte, error) {
	if r.ContentLength > maxPayloadSize {
		return nil, fmt.Errorf("payload too large: %d bytes (max %d)", r.ContentLength, maxPayloadSize)
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxPayloadSize))
	if err != nil {
		return nil, err
	}
	_ = r.Body.Close()
	return body, nil
}

func validateHMAC(secret, body []byte, sig, prefix string) bool {
	if !strings.HasPrefix(sig, prefix) {
		return false
	}
	expected := hmac.New(sha256.New, secret)
	expected.Write(body)
	got := hex.EncodeToString(expected.Sum(nil))
	return hmac.Equal([]byte(sig[len(prefix):]), []byte(got))
}

func extractParams(mappings []runnersv1alpha1.ParameterMapping, payload map[string]any) (map[string]string, error) {
	out := make(map[string]string, len(mappings))
	for _, m := range mappings {
		val := extractDotPath(payload, strings.TrimPrefix(m.Source, "$."))
		if val == "" && m.Required {
			return nil, fmt.Errorf("required parameter %q (source: %s) not found in payload", m.Name, m.Source)
		}
		if val == "" {
			val = m.Default
		}
		if val != "" {
			if m.Sanitize {
				val = sanitizeValue(val)
			}
			out[m.Name] = val
		}
	}
	return out, nil
}

func extractDotPath(data map[string]any, path string) string {
	parts := strings.Split(path, ".")
	current := any(data)
	for _, part := range parts {
		m, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = m[part]
		if current == nil {
			return ""
		}
	}
	switch v := current.(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%v", v)
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func sanitizeValue(s string) string {
	replacer := strings.NewReplacer(
		";", "", "|", "", "&", "", "$", "",
		"`", "", "(", "", ")", "", "{", "",
		"}", "", "<", "", ">", "", "\n", "",
		"\r", "", "\t", "", "'", "", "\"", "",
	)
	return replacer.Replace(s)
}

func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lrw := &loggingResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(lrw, r)
		log.Log.Info("Webhook request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", lrw.statusCode,
			"duration", time.Since(start).String(),
		)
	})
}

type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (lrw *loggingResponseWriter) WriteHeader(code int) {
	lrw.statusCode = code
	lrw.ResponseWriter.WriteHeader(code)
}
