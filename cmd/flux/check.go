/*
Copyright 2020 The Flux authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/blang/semver/v4"
	"github.com/fluxcd/flux2/internal/utils"
	"github.com/spf13/cobra"
	apimachineryversion "k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/kubernetes"
)

var checkCmd = &cobra.Command{
	Use:   "check",
	Short: "Check requirements and installation",
	Long: `The check command will perform a series of checks to validate that
the local environment is configured correctly and if the installed components are healthy.`,
	Example: `  # Run pre-installation checks
  flux check --pre

  # Run installation checks
  flux check
`,
	RunE: runCheckCmd,
}

type checkFlags struct {
	pre             bool
	components      []string
	extraComponents []string
}

type kubectlVersion struct {
	ClientVersion *apimachineryversion.Info `json:"clientVersion"`
}

var checkArgs checkFlags

func init() {
	checkCmd.Flags().BoolVarP(&checkArgs.pre, "pre", "", false,
		"only run pre-installation checks")
	checkCmd.Flags().StringSliceVar(&checkArgs.components, "components", rootArgs.defaults.Components,
		"list of components, accepts comma-separated values")
	checkCmd.Flags().StringSliceVar(&checkArgs.extraComponents, "components-extra", nil,
		"list of components in addition to those supplied or defaulted, accepts comma-separated values")
	rootCmd.AddCommand(checkCmd)
}

func runCheckCmd(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), rootArgs.timeout)
	defer cancel()

	logger.Actionf("checking prerequisites")
	checkFailed := false

	if !kubectlCheck(ctx, ">=1.18.0") {
		checkFailed = true
	}

	if !kubernetesCheck(">=1.16.0") {
		checkFailed = true
	}

	if checkArgs.pre {
		if checkFailed {
			os.Exit(1)
		}
		logger.Successf("prerequisites checks passed")
		return nil
	}

	logger.Actionf("checking controllers")
	if !componentsCheck() {
		checkFailed = true
	}
	if checkFailed {
		os.Exit(1)
	}
	logger.Successf("all checks passed")
	return nil
}

func kubectlCheck(ctx context.Context, version string) bool {
	_, err := exec.LookPath("kubectl")
	if err != nil {
		logger.Failuref("kubectl not found")
		return false
	}

	kubectlArgs := []string{"version", "--client", "--output", "json"}
	output, err := utils.ExecKubectlCommand(ctx, utils.ModeCapture, rootArgs.kubeconfig, rootArgs.kubecontext, kubectlArgs...)
	if err != nil {
		logger.Failuref("kubectl version can't be determined")
		return false
	}

	kv := &kubectlVersion{}
	if err = json.Unmarshal([]byte(output), kv); err != nil {
		logger.Failuref("kubectl version output can't be unmarshaled")
		return false
	}

	v, err := semver.ParseTolerant(kv.ClientVersion.GitVersion)
	if err != nil {
		logger.Failuref("kubectl version can't be parsed")
		return false
	}

	rng, _ := semver.ParseRange(version)
	if !rng(v) {
		logger.Failuref("kubectl version must be %s", version)
		return false
	}

	logger.Successf("kubectl %s %s", v.String(), version)
	return true
}

func kubernetesCheck(version string) bool {
	cfg, err := utils.KubeConfig(rootArgs.kubeconfig, rootArgs.kubecontext)
	if err != nil {
		logger.Failuref("Kubernetes client initialization failed: %s", err.Error())
		return false
	}

	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		logger.Failuref("Kubernetes client initialization failed: %s", err.Error())
		return false
	}

	ver, err := client.Discovery().ServerVersion()
	if err != nil {
		logger.Failuref("Kubernetes API call failed: %s", err.Error())
		return false
	}

	v, err := semver.ParseTolerant(ver.String())
	if err != nil {
		logger.Failuref("Kubernetes version can't be determined")
		return false
	}

	rng, _ := semver.ParseRange(version)
	if !rng(v) {
		logger.Failuref("Kubernetes version must be %s", version)
		return false
	}

	logger.Successf("Kubernetes %s %s", v.String(), version)
	return true
}

func componentsCheck() bool {
	ctx, cancel := context.WithTimeout(context.Background(), rootArgs.timeout)
	defer cancel()

	statusChecker, err := NewStatusChecker(time.Second, 30*time.Second)
	if err != nil {
		return false
	}

	ok := true
	deployments := append(checkArgs.components, checkArgs.extraComponents...)
	for _, deployment := range deployments {
		if err := statusChecker.Assess(deployment); err != nil {
			ok = false
		} else {
			logger.Successf("%s: healthy", deployment)
		}

		kubectlArgs := []string{"-n", rootArgs.namespace, "get", "deployment", deployment, "-o", "jsonpath=\"{..image}\""}
		if output, err := utils.ExecKubectlCommand(ctx, utils.ModeCapture, rootArgs.kubeconfig, rootArgs.kubecontext, kubectlArgs...); err == nil {
			logger.Actionf(strings.TrimPrefix(strings.TrimSuffix(output, "\""), "\""))
		}
	}
	return ok
}
