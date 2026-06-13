//go:build e2e
// +build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/mohamedhabas11/runner_operator/test/utils"
)

const namespace = "runner-operator-system"

const testNamespace = "runner-operator-test"

const serviceAccountName = "runner-operator-controller-manager"

const metricsServiceName = "runner-operator-controller-manager-metrics-service"

const metricsRoleBindingName = "runner-operator-metrics-binding"

var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string

	BeforeAll(func() {
		By("creating manager namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

		By("labeling the namespace to enforce the restricted security policy")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		By("installing CRDs")
		cmd = exec.Command("make", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", managerImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")

		By("creating test namespace for CRD resources")
		cmd = exec.Command("kubectl", "create", "ns", testNamespace)
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create test namespace")
	})

	AfterAll(func() {
		By("deleting metrics ClusterRoleBinding")
		cmd := exec.Command("kubectl", "delete", "clusterrolebinding", metricsRoleBindingName, "--ignore-not-found")
		_, _ = utils.Run(cmd)

		By("force-deleting the controller pod to skip termination grace period")
		cmd = exec.Command("kubectl", "delete", "pod", "-n", namespace,
			"-l", "control-plane=controller-manager",
			"--force", "--grace-period=0", "--ignore-not-found")
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager")
		cmd = exec.Command("make", "undeploy")
		_, _ = utils.Run(cmd)

		By("uninstalling CRDs")
		cmd = exec.Command("make", "uninstall")
		_, _ = utils.Run(cmd)

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace, "--ignore-not-found")
		_, _ = utils.Run(cmd)
	})

	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Fetching controller manager pod logs")
			cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
			controllerLogs, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerLogs)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
			}

			By("Fetching Kubernetes events")
			cmd = exec.Command("kubectl", "get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
			eventsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
			}

			By("Fetching curl-metrics logs")
			cmd = exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
			metricsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Metrics logs:\n %s", metricsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s", err)
			}

			By("Fetching controller manager pod description")
			cmd = exec.Command("kubectl", "describe", "pod", controllerPodName, "-n", namespace)
			podDescription, err := utils.Run(cmd)
			if err == nil {
				fmt.Println("Pod description:\n", podDescription)
			} else {
				fmt.Println("Failed to describe controller pod")
			}
		}
	})

	SetDefaultEventuallyTimeout(30 * time.Second)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Manager", func() {
		It("should run successfully", func() {
			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func(g Gomega) {
				By("getting the name of the controller-manager pod")
				cmd := exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")
				podNames := utils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(1), "expected 1 controller pod running")
				controllerPodName = podNames[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				By("validating the pod's status")
				cmd = exec.Command("kubectl", "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"), "Incorrect controller-manager pod status")
			}
			Eventually(verifyControllerUp).Should(Succeed())
		})

		It("should ensure the metrics endpoint is serving metrics", func() {
			By("creating a ClusterRoleBinding for the service account to allow access to metrics")
			cmd := exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
				"--clusterrole=runner-operator-metrics-reader",
				fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
			)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")

			By("validating that the metrics service is available")
			cmd = exec.Command("kubectl", "get", "service", metricsServiceName, "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("getting the service account token")
			token, err := serviceAccountToken()
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("ensuring the controller pod is ready")
			verifyControllerPodReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pod", controllerPodName, "-n", namespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"), "Controller pod not ready")
			}
			Eventually(verifyControllerPodReady, 30*time.Second, time.Second).Should(Succeed())

			By("verifying that the controller manager is serving the metrics server")
			verifyMetricsServerStarted := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("Serving metrics server"),
					"Metrics server not yet started")
			}
			Eventually(verifyMetricsServerStarted, 30*time.Second, time.Second).Should(Succeed())

			By("creating the curl-metrics pod to access the metrics endpoint")
			cmd = exec.Command("kubectl", "run", "curl-metrics", "--restart=Never",
				"--namespace", namespace,
				"--image=curlimages/curl:latest",
				"--overrides",
				fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
							"command": ["/bin/sh", "-c"],
							"args": [
								"for i in $(seq 1 30); do curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics && exit 0 || sleep 2; done; exit 1"
							],
							"securityContext": {
								"readOnlyRootFilesystem": true,
								"allowPrivilegeEscalation": false,
								"capabilities": {
									"drop": ["ALL"]
								},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {
									"type": "RuntimeDefault"
								}
							}
						}],
						"serviceAccountName": "%s"
					}
				}`, token, metricsServiceName, namespace, serviceAccountName))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics pod")

			By("waiting for the curl-metrics pod to complete.")
			verifyCurlUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "curl-metrics",
					"-o", "jsonpath={.status.phase}",
					"-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"), "curl pod in wrong status")
			}
			Eventually(verifyCurlUp, 60*time.Second).Should(Succeed())

			By("getting the metrics by checking curl-metrics logs")
			verifyMetricsAvailable := func(g Gomega) {
				metricsOutput, err := getMetricsOutput()
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
				g.Expect(metricsOutput).NotTo(BeEmpty())
				g.Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
			}
			Eventually(verifyMetricsAvailable, 30*time.Second).Should(Succeed())

			By("cleaning up the curl-metrics pod")
			cmd = exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", namespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})
	})

	Context("RunnerGit", Ordered, func() {
		const runnerName = "e2e-runner-git"

		AfterAll(func() {
			By("deleting the Runner resource")
			cmd := exec.Command("kubectl", "delete", "runner", runnerName, "-n", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)

			By("deleting the associated Job")
			cmd = exec.Command("kubectl", "delete", "job", runnerName+"-job", "-n", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		It("should clone a public repo and run a command from it", func() {
			By("applying a Runner with gitRepo")
			applyRunner := fmt.Sprintf(`
apiVersion: runners.runner-operator.io/v1alpha1
kind: Runner
metadata:
  name: %s
  namespace: %s
spec:
  image: busybox:latest
  command: ["sh", "-c"]
  args: ["ls -la /workspace/repo/README"]
  gitRepo:
    url: https://github.com/octocat/hello-world.git
    revision: master
  timeoutAfter: "5m"
`, runnerName, testNamespace)
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(applyRunner)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply Runner with gitRepo")

			By("waiting for the Job to be created")
			verifyJobCreated := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "job", runnerName+"-job", "-n", testNamespace,
					"-o", "jsonpath={.metadata.name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal(runnerName + "-job"))
			}
			Eventually(verifyJobCreated).Should(Succeed())

			By("waiting for the Runner to complete successfully")
			verifyRunnerSucceeded := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "runner", runnerName, "-n", testNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"))
			}
			Eventually(verifyRunnerSucceeded, 30*time.Second).Should(Succeed())

			By("verifying the Job has an init container for git clone")
			verifyInitContainer := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "job", runnerName+"-job", "-n", testNamespace,
					"-o", "jsonpath={.spec.template.spec.initContainers[0].name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("git-clone"))
			}
			Eventually(verifyInitContainer).Should(Succeed())

			By("verifying the runner container has a working directory")
			verifyWorkingDir := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "job", runnerName+"-job", "-n", testNamespace,
					"-o", "jsonpath={.spec.template.spec.containers[0].workingDir}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("/workspace/repo"))
			}
			Eventually(verifyWorkingDir).Should(Succeed())
		})
	})

	Context("Runner", Ordered, func() {
		const runnerName = "e2e-runner"

		AfterAll(func() {
			By("deleting the Runner resource")
			cmd := exec.Command("kubectl", "delete", "runner", runnerName, "-n", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)

			By("deleting the associated Job")
			cmd = exec.Command("kubectl", "delete", "job", runnerName+"-job", "-n", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		It("should create a Job and transition to Succeeded", func() {
			By("applying a Runner resource")
			applyRunner := fmt.Sprintf(`
apiVersion: runners.runner-operator.io/v1alpha1
kind: Runner
metadata:
  name: %s
  namespace: %s
spec:
  image: busybox:latest
  command: ["sh", "-c"]
  args: ["echo 'runner e2e test' && sleep 2"]
  timeoutAfter: "30s"
`, runnerName, testNamespace)
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(applyRunner)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply Runner")

			By("waiting for the Job to be created")
			verifyJobCreated := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "job", runnerName+"-job", "-n", testNamespace,
					"-o", "jsonpath={.metadata.name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal(runnerName + "-job"))
			}
			Eventually(verifyJobCreated).Should(Succeed())

			By("waiting for the Runner to complete successfully")
			verifyRunnerSucceeded := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "runner", runnerName, "-n", testNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"))
			}
			Eventually(verifyRunnerSucceeded, 30*time.Second).Should(Succeed())

			By("verifying the Job also completed")
			verifyJobSucceeded := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "job", runnerName+"-job", "-n", testNamespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Complete')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"))
			}
			Eventually(verifyJobSucceeded).Should(Succeed())
		})
	})

	Context("RunnerFailure", Ordered, func() {
		const runnerName = "e2e-runner-fail"

		AfterAll(func() {
			By("deleting the Runner resource")
			cmd := exec.Command("kubectl", "delete", "runner", runnerName, "-n", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)

			By("deleting the associated Job")
			cmd = exec.Command("kubectl", "delete", "job", runnerName+"-job", "-n", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		It("should transition to Failed when the command exits non-zero", func() {
			By("applying a Runner that fails")
			applyRunner := fmt.Sprintf(`
apiVersion: runners.runner-operator.io/v1alpha1
kind: Runner
metadata:
  name: %s
  namespace: %s
spec:
  image: busybox:latest
  command: ["sh", "-c"]
  args: ["echo 'about to fail' && exit 1"]
  timeoutAfter: "30s"
`, runnerName, testNamespace)
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(applyRunner)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply Runner")

			By("waiting for the Runner to transition to Failed")
			verifyRunnerFailed := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "runner", runnerName, "-n", testNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Failed"))
			}
			Eventually(verifyRunnerFailed, 30*time.Second).Should(Succeed())

			By("verifying the Job also reports failure")
			verifyJobFailed := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "job", runnerName+"-job", "-n", testNamespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Failed')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"))
			}
			Eventually(verifyJobFailed).Should(Succeed())
		})
	})

	Context("SpecDrift", Ordered, func() {
		const runnerName = "e2e-spec-drift"

		AfterAll(func() {
			By("deleting the Runner resource")
			cmd := exec.Command("kubectl", "delete", "runner", runnerName, "-n", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)

			By("deleting the associated Job")
			cmd = exec.Command("kubectl", "delete", "job", runnerName+"-job", "-n", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		It("should recreate the Job when spec changes after completion", func() {
			By("applying a Runner with initial spec")
			applyRunner := fmt.Sprintf(`
apiVersion: runners.runner-operator.io/v1alpha1
kind: Runner
metadata:
  name: %s
  namespace: %s
spec:
  image: busybox:latest
  command: ["sh", "-c"]
  args: ["echo first-run && sleep 2"]
  timeoutAfter: "30s"
`, runnerName, testNamespace)
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(applyRunner)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply Runner")

			By("waiting for initial Runner to succeed")
			verifyRunnerSucceeded := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "runner", runnerName, "-n", testNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"))
			}
			Eventually(verifyRunnerSucceeded, 30*time.Second).Should(Succeed())

			By("recording the old Job UID")
			getOldJobUID := func() string {
				cmd := exec.Command("kubectl", "get", "job", runnerName+"-job", "-n", testNamespace,
					"-o", "jsonpath={.metadata.uid}")
				output, err := utils.Run(cmd)
				Expect(err).NotTo(HaveOccurred())
				return strings.TrimSpace(output)
			}
			oldUID := getOldJobUID()

			By("updating the Runner spec with a different command")
			updateRunner := fmt.Sprintf(`
apiVersion: runners.runner-operator.io/v1alpha1
kind: Runner
metadata:
  name: %s
  namespace: %s
spec:
  image: busybox:latest
  command: ["sh", "-c"]
  args: ["echo second-run && sleep 1"]
  timeoutAfter: "30s"
`, runnerName, testNamespace)
			cmd = exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(updateRunner)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to update Runner")

			By("waiting for the Job to be recreated with a new UID")
			verifyJobRecreated := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "job", runnerName+"-job", "-n", testNamespace,
					"-o", "jsonpath={.metadata.uid}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				newUID := strings.TrimSpace(output)
				g.Expect(newUID).NotTo(BeEmpty())
				g.Expect(newUID).NotTo(Equal(oldUID), "Job should have been recreated with a new UID")
			}
			Eventually(verifyJobRecreated, 30*time.Second).Should(Succeed())

			By("verifying Runner eventually succeeds again with new spec")
			verifyRunnerSucceededAgain := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "runner", runnerName, "-n", testNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"))
			}
			Eventually(verifyRunnerSucceededAgain, 30*time.Second).Should(Succeed())
		})
	})

	Context("Workflow", Ordered, func() {
		const workflowName = "e2e-workflow"

		AfterAll(func() {
			By("deleting the Workflow resource")
			cmd := exec.Command("kubectl", "delete", "workflow", workflowName, "-n", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)

			By("deleting any Runner resources created by the Workflow")
			cmd = exec.Command("kubectl", "delete", "runner", "-l",
				"runner-operator.io/workflow="+workflowName, "-n", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		It("should execute all steps in dependency order", func() {
			By("applying a Workflow with chained steps")
			applyWorkflow := fmt.Sprintf(`
apiVersion: runners.runner-operator.io/v1alpha1
kind: Workflow
metadata:
  name: %s
  namespace: %s
spec:
  steps:
    - name: step-one
      image: busybox:latest
      command: ["sh", "-c"]
      args: ["echo 'workflow step one' && sleep 2"]
      timeout: "30s"
    - name: step-two
      image: busybox:latest
      command: ["sh", "-c"]
      args: ["echo 'workflow step two' && sleep 1"]
      dependsOn: ["step-one"]
      when: "on_success"
      timeout: "30s"
`, workflowName, testNamespace)
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(applyWorkflow)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply Workflow")

			By("waiting for step-one Runner to be created")
			stepOneRunner := fmt.Sprintf("%s-step-one", workflowName)
			verifyStepOneRunner := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "runner", stepOneRunner, "-n", testNamespace,
					"-o", "jsonpath={.metadata.name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal(stepOneRunner))
			}
			Eventually(verifyStepOneRunner).Should(Succeed())

			By("waiting for step-one to succeed")
			verifyStepOneSucceeded := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "runner", stepOneRunner, "-n", testNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"))
			}
			Eventually(verifyStepOneSucceeded, 30*time.Second).Should(Succeed())

			By("waiting for step-two Runner to be created (depends on step-one)")
			stepTwoRunner := fmt.Sprintf("%s-step-two", workflowName)
			verifyStepTwoRunner := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "runner", stepTwoRunner, "-n", testNamespace,
					"-o", "jsonpath={.metadata.name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal(stepTwoRunner))
			}
			Eventually(verifyStepTwoRunner).Should(Succeed())

			By("waiting for step-two to succeed")
			verifyStepTwoSucceeded := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "runner", stepTwoRunner, "-n", testNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"))
			}
			Eventually(verifyStepTwoSucceeded, 30*time.Second).Should(Succeed())

			By("verifying the Workflow status is Succeeded")
			verifyWorkflowSucceeded := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "workflow", workflowName, "-n", testNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"))
			}
			Eventually(verifyWorkflowSucceeded).Should(Succeed())

			By("verifying step statuses reflect correct order")
			verifyStepStatuses := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "workflow", workflowName, "-n", testNamespace,
					"-o", "jsonpath={.status.stepStatuses}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("step-one"))
				g.Expect(output).To(ContainSubstring("step-two"))
				g.Expect(output).To(ContainSubstring("Succeeded"))
			}
			Eventually(verifyStepStatuses).Should(Succeed())
		})
	})

	Context("WorkflowTimeout", Ordered, func() {
		const wfName = "e2e-wf-timeout"

		AfterAll(func() {
			By("deleting the Workflow resource")
			cmd := exec.Command("kubectl", "delete", "workflow", wfName, "-n", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)

			By("deleting any Runner resources created by the Workflow")
			cmd = exec.Command("kubectl", "delete", "runner", "-l",
				"runner-operator.io/workflow="+wfName, "-n", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		It("should fail when the workflow timeout is exceeded", func() {
			By("applying a Workflow with a short timeout")
			applyWF := fmt.Sprintf(`
apiVersion: runners.runner-operator.io/v1alpha1
kind: Workflow
metadata:
  name: %s
  namespace: %s
spec:
  timeout: "10s"
  steps:
    - name: long-step
      image: busybox:latest
      command: ["sh", "-c"]
      args: ["echo starting && sleep 120"]
      timeout: "5m"
`, wfName, testNamespace)
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(applyWF)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply Workflow")

			By("waiting for the Workflow to transition to Failed due to timeout")
			verifyWorkflowFailed := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "workflow", wfName, "-n", testNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Failed"))
			}
			Eventually(verifyWorkflowFailed, 30*time.Second).Should(Succeed())

			By("verifying the completionTime is set")
			verifyCompletionTime := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "workflow", wfName, "-n", testNamespace,
					"-o", "jsonpath={.status.completionTime}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).NotTo(BeEmpty())
			}
			Eventually(verifyCompletionTime).Should(Succeed())
		})
	})

	Context("WorkflowOnFailure", Ordered, func() {
		const wfName = "e2e-wf-onfailure"

		AfterAll(func() {
			By("deleting the Workflow resource")
			cmd := exec.Command("kubectl", "delete", "workflow", wfName, "-n", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)

			By("deleting any Runner resources created by the Workflow")
			cmd = exec.Command("kubectl", "delete", "runner", "-l",
				"runner-operator.io/workflow="+wfName, "-n", testNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		It("should run the on_failure step when a dependency fails", func() {
			By("applying a Workflow with a failing step and an on_failure handler")
			applyWF := fmt.Sprintf(`
apiVersion: runners.runner-operator.io/v1alpha1
kind: Workflow
metadata:
  name: %s
  namespace: %s
spec:
  steps:
    - name: fail-step
      image: busybox:latest
      command: ["sh", "-c"]
      args: ["echo failing && exit 1"]
      timeout: "30s"
    - name: cleanup-step
      image: busybox:latest
      command: ["sh", "-c"]
      args: ["echo cleaning up after failure && sleep 1"]
      dependsOn: ["fail-step"]
      when: "on_failure"
      timeout: "30s"
`, wfName, testNamespace)
			cmd := exec.Command("kubectl", "apply", "-f", "-")
			cmd.Stdin = strings.NewReader(applyWF)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply Workflow")

			By("waiting for fail-step Runner to be created")
			failStepRunner := fmt.Sprintf("%s-fail-step", wfName)
			verifyFailStepRunner := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "runner", failStepRunner, "-n", testNamespace,
					"-o", "jsonpath={.metadata.name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal(failStepRunner))
			}
			Eventually(verifyFailStepRunner).Should(Succeed())

			By("waiting for fail-step to fail")
			verifyFailStepFailed := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "runner", failStepRunner, "-n", testNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Failed"))
			}
			Eventually(verifyFailStepFailed, 30*time.Second).Should(Succeed())

			By("waiting for cleanup-step Runner to be created")
			cleanupStepRunner := fmt.Sprintf("%s-cleanup-step", wfName)
			verifyCleanupRunner := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "runner", cleanupStepRunner, "-n", testNamespace,
					"-o", "jsonpath={.metadata.name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal(cleanupStepRunner))
			}
			Eventually(verifyCleanupRunner).Should(Succeed())

			By("waiting for cleanup-step to succeed")
			verifyCleanupSucceeded := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "runner", cleanupStepRunner, "-n", testNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"))
			}
			Eventually(verifyCleanupSucceeded, 30*time.Second).Should(Succeed())

			By("verifying the Workflow status is Failed (at least one step failed)")
			verifyWorkflowFailed := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "workflow", wfName, "-n", testNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Failed"))
			}
			Eventually(verifyWorkflowFailed).Should(Succeed())

			By("verifying step statuses include both steps")
			verifyStepStatuses := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "workflow", wfName, "-n", testNamespace,
					"-o", "jsonpath={.status.stepStatuses}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("fail-step"))
				g.Expect(output).To(ContainSubstring("cleanup-step"))
				g.Expect(output).To(ContainSubstring("Succeeded"))
			}
			Eventually(verifyStepStatuses).Should(Succeed())
		})
	})
})

func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	By("creating temporary file to store the token request")
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		By("executing kubectl command to create the token")
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace,
			serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		By("parsing the JSON output to extract the token")
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

func getMetricsOutput() (string, error) {
	By("getting the curl-metrics logs")
	cmd := exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
	return utils.Run(cmd)
}

type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}
