package integration

import (
	"fmt"
	"os"
	"time"

	"github.com/devfile/library/v2/pkg/util"
	"github.com/google/go-github/v44/github"
	"github.com/konflux-ci/e2e-tests/pkg/clients/has"
	"github.com/konflux-ci/e2e-tests/pkg/constants"
	"github.com/konflux-ci/e2e-tests/pkg/framework"
	"github.com/konflux-ci/e2e-tests/pkg/utils"

	appstudioApi "github.com/konflux-ci/application-api/api/v1alpha1"
	integrationv1beta2 "github.com/konflux-ci/integration-service/api/v1beta2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	pipeline "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
)

var _ = framework.IntegrationServiceSuiteDescribe("Creation of group snapshots for monorepo and multiple repos", Label("integration-service", "group-snapshot-creation"), func() {
	defer GinkgoRecover()

	var f *framework.Framework
	var err error

	var prNumber int
	var timeout, interval time.Duration
	var prHeadSha, mergeResultSha string
	var pacBranchNames []string
	var snapshot *appstudioApi.Snapshot
	var componentA *appstudioApi.Component
	var componentB *appstudioApi.Component
	var mergeResult *github.PullRequestMergeResult
	var pipelineRun, testPipelinerun *pipeline.PipelineRun
	var integrationTestScenarioPass *integrationv1beta2.IntegrationTestScenario
	var applicationName, testNamespace, multiComponentBaseBranchName, multiComponentPRBranchName string

	AfterEach(framework.ReportFailure(&f))

	Describe("with status reporting of Integration tests in CheckRuns", Ordered, func() {
		BeforeAll(func() {
			if os.Getenv(constants.SKIP_PAC_TESTS_ENV) == "true" {
				Skip("Skipping this test due to configuration issue with Spray proxy")
			}

			f, err = framework.NewFramework(utils.GetGeneratedNamespace("group"))
			Expect(err).NotTo(HaveOccurred())
			testNamespace = f.UserNamespace

			if utils.IsPrivateHostname(f.OpenshiftConsoleHost) {
				Skip("Using private cluster (not reachable from Github), skipping...")
			}

			applicationName = createApp(*f, testNamespace)

			// The Base branch or a ToBranch where all multi-component definitions will live
			multiComponentBaseBranchName = fmt.Sprintf("love-triangle-%s", util.GenerateRandomString(6))
			err = f.AsKubeAdmin.CommonController.Github.CreateRef(multiComponentRepoNameForGroupSnapshot, multiComponentDefaultBranch, multiComponentGitRevision, multiComponentBaseBranchName)
			Expect(err).ShouldNot(HaveOccurred())

			//Branch for creating pull request
			multiComponentPRBranchName = fmt.Sprintf("%s-%s", "pr-branch", util.GenerateRandomString(6))

			integrationTestScenarioPass, err = f.AsKubeAdmin.IntegrationController.CreateIntegrationTestScenario("", applicationName, testNamespace, gitURL, revision, pathInRepoPass, []string{})
			Expect(err).ShouldNot(HaveOccurred())
		})

		AfterAll(func() {
			// if !CurrentSpecReport().Failed() {
			// 	cleanup(*f, testNamespace, applicationName, componentA.Name)
			// 	cleanup(*f, testNamespace, applicationName, componentB.Name)
			// }

			// // Delete new branches created by PaC and a testing branch used as a component's base branch
			// for _, pacBranchName := range pacBranchNames {
			// 	err = f.AsKubeAdmin.CommonController.Github.DeleteRef(multiComponentRepoNameForGroupSnapshot, pacBranchName)
			// 	if err != nil {
			// 		Expect(err.Error()).To(ContainSubstring("Reference does not exist"))
			// 	}
			// }
			// // Delete the created base branch
			// err = f.AsKubeAdmin.CommonController.Github.DeleteRef(multiComponentRepoNameForGroupSnapshot, multiComponentBaseBranchName)
			// if err != nil {
			// 	Expect(err.Error()).To(ContainSubstring(referenceDoesntExist))
			// }
			// // Delete the created pr branch
			// err = f.AsKubeAdmin.CommonController.Github.DeleteRef(multiComponentRepoNameForGroupSnapshot, multiComponentPRBranchName)
			// if err != nil {
			// 	Expect(err.Error()).To(ContainSubstring(referenceDoesntExist))
			// }
		})

		/*  /\
		   /  \
		  / /\ \
		 / ____ \
		/_/    \_\ */
		When("we start creation of a new Component A", func() {
			It("creates the Component A successfully", func() {
				componentA = createComponentWithCustomBranch(*f, testNamespace, applicationName, multiComponentContextDirs[0]+"-"+util.GenerateRandomString(6), multiComponentGitSourceURLForGroupSnapshotA, multiComponentBaseBranchName, multiComponentContextDirs[0])

				// Record the PaC branch names for cleanup
				pacBranchName := constants.PaCPullRequestBranchPrefix + componentA.Name
				pacBranchNames = append(pacBranchNames, pacBranchName)
			})

			It(fmt.Sprintf("triggers a Build PipelineRun for component %s", multiComponentContextDirs[0]), func() {
				timeout = time.Second * 600
				interval = time.Second * 1
				Eventually(func() error {
					pipelineRun, err = f.AsKubeAdmin.HasController.GetComponentPipelineRun(componentA.Name, applicationName, testNamespace, "")
					if err != nil {
						GinkgoWriter.Printf("Build PipelineRun has not been created yet for the componentA %s/%s\n", testNamespace, componentA.Name)
						return err
					}
					if !pipelineRun.HasStarted() {
						return fmt.Errorf("build pipelinerun %s/%s hasn't started yet", pipelineRun.GetNamespace(), pipelineRun.GetName())
					}
					return nil
				}, timeout, constants.PipelineRunPollingInterval).Should(Succeed(), fmt.Sprintf("timed out when waiting for the build PipelineRun to start for the componentA %s/%s", testNamespace, componentA.Name))
			})

			It("does not contain an annotation with a Snapshot Name", func() {
				Expect(pipelineRun.Annotations[snapshotAnnotation]).To(Equal(""))
			})

			It("should lead to build PipelineRun finishing successfully", func() {
				Expect(f.AsKubeAdmin.HasController.WaitForComponentPipelineToBeFinished(componentA,
					"", f.AsKubeAdmin.TektonController, &has.RetryOptions{Retries: 2, Always: true}, pipelineRun)).To(Succeed())
			})

			It(fmt.Sprintf("should lead to a PaC PR creation for component %s", multiComponentContextDirs[0]), func() {
				timeout = time.Second * 300
				interval = time.Second * 1

				Eventually(func() bool {
					prs, err := f.AsKubeAdmin.CommonController.Github.ListPullRequests(multiComponentRepoNameForGroupSnapshot)
					Expect(err).ShouldNot(HaveOccurred())

					for _, pr := range prs {
						if pr.Head.GetRef() == pacBranchNames[0] {
							prNumber = pr.GetNumber()
							prHeadSha = pr.Head.GetSHA()
							return true
						}
					}
					return false
				}, timeout, interval).Should(BeTrue(), fmt.Sprintf("timed out when waiting for init PaC PR (branch name '%s') to be created in %s repository", pacBranchNames[0], multiComponentRepoNameForGroupSnapshot))

				// in case the first pipelineRun attempt has failed and was retried, we need to update the value of pipelineRun variable
				pipelineRun, err = f.AsKubeAdmin.HasController.GetComponentPipelineRun(componentA.Name, applicationName, testNamespace, prHeadSha)
				Expect(err).ShouldNot(HaveOccurred())
			})

			It("eventually leads to the build PipelineRun's status reported at Checks tab", func() {
				expectedCheckRunName := fmt.Sprintf("%s-%s", componentA.Name, "on-pull-request")
				Expect(f.AsKubeAdmin.CommonController.Github.GetCheckRunConclusion(expectedCheckRunName, multiComponentRepoNameForGroupSnapshot, prHeadSha, prNumber)).To(Equal(constants.CheckrunConclusionSuccess))
			})
		})

		When("the Build PLR is finished successfully", func() {
			It("checks if the Snapshot is created", func() {
				snapshot, err = f.AsKubeDeveloper.IntegrationController.WaitForSnapshotToGetCreated("", pipelineRun.Name, componentA.Name, testNamespace)
				Expect(err).ShouldNot(HaveOccurred())
			})

			It("should find the related Integration PipelineRuns", func() {
				testPipelinerun, err = f.AsKubeDeveloper.IntegrationController.WaitForIntegrationPipelineToGetStarted(integrationTestScenarioPass.Name, snapshot.Name, testNamespace)
				Expect(err).ToNot(HaveOccurred())
				Expect(testPipelinerun.Labels[snapshotAnnotation]).To(ContainSubstring(snapshot.Name))
				Expect(testPipelinerun.Labels[scenarioAnnotation]).To(ContainSubstring(integrationTestScenarioPass.Name))
			})

			// TODO: Should we wait for Intg PLR(s) to finish successfully?
		})

		When("the Snapshot testing is completed successfully", func() {
			It("should merge the init PaC PR successfully", func() {
				Eventually(func() error {
					mergeResult, err = f.AsKubeAdmin.CommonController.Github.MergePullRequest(multiComponentRepoNameForGroupSnapshot, prNumber)
					return err
				}, time.Minute).Should(BeNil(), fmt.Sprintf("error when merging PaC pull request #%d in repo %s", prNumber, multiComponentRepoNameForGroupSnapshot))

				mergeResultSha = mergeResult.GetSHA()
				GinkgoWriter.Printf("merged result sha: %s for PR #%d\n", mergeResultSha, prNumber)
			})

			// TODO: Should we wait for "push"-type" Build PLR to finish successfully?
		})

		/*____
		|  _ \
		| |_) |
		|  _ <
		| |_) |
		|____/ */
		When("we start creation of a new Component B", func() {
			It("creates the Component B successfully", func() {
				componentB = createComponentWithCustomBranch(*f, testNamespace, applicationName, multiComponentContextDirs[1] + "-" + util.GenerateRandomString(6), multiComponentGitSourceURLForGroupSnapshotB, multiComponentBaseBranchName, multiComponentContextDirs[1])

				// Recording the PaC branch names so they can cleaned in the AfterAll block
				pacBranchName := constants.PaCPullRequestBranchPrefix + componentB.Name
				pacBranchNames = append(pacBranchNames, pacBranchName)
			})

			It(fmt.Sprintf("triggers a Build PipelineRun for component %s", multiComponentContextDirs[1]), func() {
				timeout = time.Second * 600
				interval = time.Second * 1
				Eventually(func() error {
					pipelineRun, err = f.AsKubeAdmin.HasController.GetComponentPipelineRun(componentB.Name, applicationName, testNamespace, "")
					if err != nil {
						GinkgoWriter.Printf("Build PipelineRun has not been created yet for the componentB %s/%s\n", testNamespace, componentB.Name)
						return err
					}
					if !pipelineRun.HasStarted() {
						return fmt.Errorf("build pipelinerun %s/%s hasn't started yet", pipelineRun.GetNamespace(), pipelineRun.GetName())
					}
					return nil
				}, timeout, constants.PipelineRunPollingInterval).Should(Succeed(), fmt.Sprintf("timed out when waiting for the build PipelineRun to start for the componentB %s/%s", testNamespace, componentB.Name))
			})

			It("does not contain an annotation with a Snapshot Name", func() {
				Expect(pipelineRun.Annotations[snapshotAnnotation]).To(Equal(""))
			})

			It("should lead to build PipelineRun finishing successfully", func() {
				Expect(f.AsKubeAdmin.HasController.WaitForComponentPipelineToBeFinished(componentB,
					"", f.AsKubeAdmin.TektonController, &has.RetryOptions{Retries: 2, Always: true}, pipelineRun)).To(Succeed())
			})

			It(fmt.Sprintf("should lead to a PaC PR creation for component %s", multiComponentContextDirs[1]), func() {
				timeout = time.Second * 300
				interval = time.Second * 1

				Eventually(func() bool {
					prs, err := f.AsKubeAdmin.CommonController.Github.ListPullRequests(multiComponentRepoNameForGroupSnapshot)
					Expect(err).ShouldNot(HaveOccurred())

					for _, pr := range prs {
						if pr.Head.GetRef() == pacBranchNames[1] {
							prNumber = pr.GetNumber()
							prHeadSha = pr.Head.GetSHA()
							return true
						}
					}
					return false
				}, timeout, interval).Should(BeTrue(), fmt.Sprintf("timed out when waiting for init PaC PR (branch name '%s') to be created in %s repository", pacBranchNames[1], multiComponentRepoNameForGroupSnapshot))

				// in case the first pipelineRun attempt has failed and was retried, we need to update the value of pipelineRun variable
				pipelineRun, err = f.AsKubeAdmin.HasController.GetComponentPipelineRun(componentB.Name, applicationName, testNamespace, prHeadSha)
				Expect(err).ShouldNot(HaveOccurred())
			})

			It("eventually leads to the build PipelineRun's status reported at Checks tab", func() {
				expectedCheckRunName := fmt.Sprintf("%s-%s", componentB.Name, "on-pull-request")
				Expect(f.AsKubeAdmin.CommonController.Github.GetCheckRunConclusion(expectedCheckRunName, multiComponentRepoNameForGroupSnapshot, prHeadSha, prNumber)).To(Equal(constants.CheckrunConclusionSuccess))
			})
		})

		When("the Build PLR is finished successfully", func() {
			It("checks if the Snapshot is created", func() {
				snapshot, err = f.AsKubeDeveloper.IntegrationController.WaitForSnapshotToGetCreated("", pipelineRun.Name, componentA.Name, testNamespace)
				Expect(err).ShouldNot(HaveOccurred())
			})

			It("should find the related Integration PipelineRuns", func() {
				testPipelinerun, err = f.AsKubeDeveloper.IntegrationController.WaitForIntegrationPipelineToGetStarted(integrationTestScenarioPass.Name, snapshot.Name, testNamespace)
				Expect(err).ToNot(HaveOccurred())
				Expect(testPipelinerun.Labels[snapshotAnnotation]).To(ContainSubstring(snapshot.Name))
				Expect(testPipelinerun.Labels[scenarioAnnotation]).To(ContainSubstring(integrationTestScenarioPass.Name))
			})

			// TODO: Should we wait for Intg PLR(s) to finish successfully?
		})

		When("the Snapshot testing is completed successfully", func() {
			It("should merge the init PaC PR successfully", func() {
				Eventually(func() error {
					mergeResult, err = f.AsKubeAdmin.CommonController.Github.MergePullRequest(multiComponentRepoNameForGroupSnapshot, prNumber)
					return err
				}, time.Minute).Should(BeNil(), fmt.Sprintf("error when merging PaC pull request #%d in repo %s", prNumber, multiComponentRepoNameForGroupSnapshot))

				mergeResultSha = mergeResult.GetSHA()
				GinkgoWriter.Printf("merged result sha: %s for PR #%d\n", mergeResultSha, prNumber)
			})

			// TODO: Should we wait for "push"-type" Build PLR to finish successfully?
		})

		When("both the init PaC PRs are merged", func() {
			It("should make change to the root folder", func() {
				// Delete all the pipelineruns in the namespace before sending PR
				// Expect(f.AsKubeAdmin.TektonController.DeleteAllPipelineRunsInASpecificNamespace(testNamespace)).To(Succeed())

				//Create the ref, add the files and create the PR
				err = f.AsKubeAdmin.CommonController.Github.CreateRef(multiComponentRepoNameForGroupSnapshot, multiComponentDefaultBranch, mergeResultSha, multiComponentPRBranchName)
				Expect(err).ShouldNot(HaveOccurred())

				fileToCreatePathForCompA := fmt.Sprintf("%s/sample-file-for-componentA.txt", multiComponentContextDirs[0])
				_, err := f.AsKubeAdmin.CommonController.Github.CreateFile(multiComponentRepoNameForGroupSnapshot, fileToCreatePathForCompA, "Living is suffering and I'm living", multiComponentPRBranchName)
				Expect(err).ShouldNot(HaveOccurred(), fmt.Sprintf("error while creating file: %s", fileToCreatePathForCompA))

				fileToCreatePathForCompB := fmt.Sprintf("%s/sample-file-for-componentB.txt", multiComponentContextDirs[1])
				createdFileSha, err := f.AsKubeAdmin.CommonController.Github.CreateFile(multiComponentRepoNameForGroupSnapshot, fileToCreatePathForCompB, "Living is suffering and I'm living", multiComponentPRBranchName)
				Expect(err).ShouldNot(HaveOccurred(), fmt.Sprintf("error while creating file: %s", fileToCreatePathForCompB))

				pr, err := f.AsKubeAdmin.CommonController.Github.CreatePullRequest(multiComponentRepoNameForGroupSnapshot, "Very Important PR", "sample pr body", multiComponentPRBranchName, multiComponentBaseBranchName)
				Expect(err).ShouldNot(HaveOccurred())
				GinkgoWriter.Printf("PR #%d got created with sha %s\n", pr.GetNumber(), createdFileSha.GetSHA())
			})
		})
	})
})
