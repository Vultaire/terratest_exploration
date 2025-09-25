package test

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/terraform"
)

func TestApplyAndDestroy(t *testing.T) {
	options := terraform.WithDefaultRetryableErrors(t, &terraform.Options{
		TerraformDir: ".",
	})

	writeVersionsTf("0.22.0")
	defer os.Remove("versions.tf")
	defer cleanup(t, options) // Fallback cleanup in case of errors
	applyTimestamp := applyAndVerify(t, options)
	queryParam := `--query=forEach(units, unit => unit.life=="alive" && unit.workload-status=="active" && unit.agent-status=="idle")`
	applyWaitTimestamp := waitAfterApply(t, queryParam, applyTimestamp)

	/*
		To do later: run verification check re: what was actually deployed.
		Maybe a "juju export-bundle" comparison tool (versus an expected state, with
		certain fields being ignored) might be a way to accomplish this.
	*/

	destroyTimestamp := destroyAndVerify(t, options, applyWaitTimestamp)
	waitAfterDestroy(t, destroyTimestamp)
}

func TestApplyUpgradeAndDestroy(t *testing.T) {
	options := terraform.WithDefaultRetryableErrors(t, &terraform.Options{
		TerraformDir: ".",
	})

	providerStartVersion := "0.22.0"
	providerUpgradeRef := "v1.0.0-beta2"

	writeVersionsTf(providerStartVersion)
	defer os.Remove("versions.tf")
	defer cleanup(t, options) // Fallback cleanup in case of errors
	applyTimestamp := applyAndVerify(t, options)
	queryParam := `--query=forEach(units, unit => unit.life=="alive" && unit.workload-status=="active" && unit.agent-status=="idle")`
	applyWaitTimestamp := waitAfterApply(t, queryParam, applyTimestamp)

	// Build custom provider
	t.Log("Creating build dir")
	buildDir, err := os.MkdirTemp("", "builddir")
	if err != nil {
		t.Fatalf("Error setting up tempdir for building custom provider: %s", err)
	}
	defer os.RemoveAll(buildDir)
	t.Logf("Building provider from source code, reference %s", providerUpgradeRef)
	terraformRcPath := buildCustomProvider(t, providerStartVersion, providerUpgradeRef, buildDir)

	// Tweak config to use the custom provider (until test teardown)
	options.EnvVars = map[string]string{
		"TF_CLI_CONFIG_FILE": terraformRcPath,
	}
	defer func() {
		options.EnvVars = nil
	}()

	reApplyTimestamp := applyAndReVerify(t, options, applyWaitTimestamp)

	/*
		To do later: run verification check re: what was actually deployed.
		Maybe a "juju export-bundle" comparison tool (versus an expected state, with
		certain fields being ignored) might be a way to accomplish this.
	*/

	destroyTimestamp := destroyAndVerify(t, options, reApplyTimestamp)
	waitAfterDestroy(t, destroyTimestamp)
}

func applyAndVerify(t *testing.T, options *terraform.Options) time.Time {
	startTime := time.Now()
	applyOutput := terraform.InitAndApply(t, options)
	timestamp := time.Now()
	duration := timestamp.Sub(startTime)
	t.Logf("Initial terraform apply time: %v\n", duration)
	verifyApply(t, applyOutput)
	return timestamp
}

func waitAfterApply(t *testing.T, queryParam string, lastTimestamp time.Time) time.Time {
	runCommand(
		t,
		[]string{"juju", "wait-for", "model", "main", queryParam},
		"",
		"Error waiting for the model to settle: %s")
	timestamp := time.Now()
	duration := timestamp.Sub(lastTimestamp)
	t.Logf("Post-apply time waiting until model settled: %v\n", duration)
	return timestamp
}

// func TestBuildProvider(t *testing.T) {
// 	buildCustomProvider(t, "v1.0.0-beta2", "0.22.0")
// }

func buildCustomProvider(t *testing.T, previousVersion string, gitRef string, buildDir string) string {
	/*
		Pseudocode:
		* Ensure we have prereqs
		* Download the code
		* Build the code
		* Replace the current provider, i.e. keep the existing versions.tf file
		  but just change the provider itself.
		* Perform any other cleanup needed.
	*/
	// Check for prereqs which aren't handled by the Makefile
	requiredExecutables := []string{"make", "yq"}
	for _, executable := range requiredExecutables {
		runCommand(t,
			append([]string{"which"}, executable),
			"",
			fmt.Sprintf("Did not find prerequisite executable: %s", executable))
	}

	t.Log("Performing shallow clone of provider repo")

	runCommand(t,
		[]string{"git", "clone", "--depth", "1", "--branch", gitRef, "https://github.com/juju/terraform-provider-juju.git"},
		buildDir,
		"Error cloning terraform-provider-juju repo: %s")

	t.Log("Building the provider")
	makeCommands := [][]string{
		{"make", "install-dependencies"},
		{"make", "go-install"}, /* builds into gopath */
	}
	for _, makeCommand := range makeCommands {
		t.Logf("Running command: %v", makeCommand)
		runCommand(t,
			makeCommand,
			filepath.Join(buildDir, "terraform-provider-juju"),
			"Error building terraform-provider-juju repo: %s")
	}

	t.Log("Getting GOPATH")
	stdout, err := exec.Command("go", "env", "GOPATH").CombinedOutput()
	if err != nil {
		t.Fatalf("Could not determine GOPATH")
	}
	goPath := strings.TrimSpace(string(stdout))

	t.Log("Creating custom Terraform RC to provide dev_override for juju/juju")
	// NOTE: It may be better to leverage TF_CLI_CONFIG_FILE for this.
	terraformRc := filepath.Join(buildDir, "dev_overrides.tfrc")
	output, err := os.OpenFile(terraformRc, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		log.Fatalf("Could not open %s: %s", terraformRc, err)
	}
	defer output.Close()
	output.WriteString(fmt.Sprintf(`provider_installation {
    dev_overrides {
        "juju/juju" = "%s"
    }
    direct {}
}`, filepath.Join(goPath, "bin")))
	return terraformRc
}

func applyAndReVerify(t *testing.T, options *terraform.Options, lastTimestamp time.Time) time.Time {
	applyOutput := terraform.InitAndApply(t, options)
	timestamp := time.Now()
	duration := timestamp.Sub(lastTimestamp)
	t.Logf("New provider terraform apply time: %v\n", duration)
	verifyReApply(t, applyOutput)
	return timestamp
}

func destroyAndVerify(t *testing.T, options *terraform.Options, lastTimestamp time.Time) time.Time {
	destroyOutput := terraform.Destroy(t, options)
	timestamp := time.Now()
	duration := timestamp.Sub(lastTimestamp)
	t.Logf("Terraform destroy time: %v\n", duration)
	verifyDestroy(t, destroyOutput)
	return timestamp
}

func waitAfterDestroy(t *testing.T, lastTimestamp time.Time) {
	runCommand(t,
		[]string{"juju", "wait-for", "model", "main"},
		"",
		"Error waiting for the model to settle: %s")
	timestamp := time.Now()
	duration := timestamp.Sub(lastTimestamp)
	t.Logf("Post-apply time waiting until model settled: %v\n", duration)
}

func runCommand(t *testing.T, cmdLine []string, dir string, errorMessage string) {
	cmd := exec.Command(cmdLine[0], cmdLine[1:]...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err != nil {
		t.Fatalf(errorMessage, err)
	}
}

func verifyApply(t *testing.T, applyOutput string) {
	// Verify that we see a successful apply.
	// e.g. "Apply complete! Resources: 2 added, 0 changed, 0 destroyed."
	rgx := regexp.MustCompile(`^Apply complete! Resources: (\d+) added, (\d+) changed, (\d+) destroyed.$`)
	scanner := bufio.NewScanner(strings.NewReader(applyOutput))
	applyCompleted := false
	for scanner.Scan() {
		groups := rgx.FindStringSubmatch(scanner.Text())
		if groups != nil {
			applyCompleted = true

			ints := []int{}
			for i := 0; i < 3; i++ {
				j, err := strconv.Atoi(groups[i+1])
				if err != nil {
					t.Fatal(err)
				}
				ints = append(ints, j)
			}
			added, changed, destroyed := ints[0], ints[1], ints[2]
			if added == 0 {
				t.Fatal(`Zero "added" count on apply`)
			}
			if changed > 0 {
				t.Fatal(`Non-zero "changed" count on apply`)
			}
			if destroyed > 0 {
				t.Fatal(`Non-zero "destroyed" count on apply`)
			}
			break
		}
	}
	if !applyCompleted {
		// Unlikely unless environment is set to a non-English locale, since
		// actual apply errors will normally be caught by terratest.
		t.Fatal(`Did not find expected string; please use "C" locale`)
	}
}

func verifyReApply(t *testing.T, applyOutput string) {
	// Verify that we see a successful apply.
	// e.g. "Apply complete! Resources: 2 added, 0 changed, 0 destroyed."
	rgx := regexp.MustCompile(`^Apply complete! Resources: (\d+) added, (\d+) changed, (\d+) destroyed.$`)
	scanner := bufio.NewScanner(strings.NewReader(applyOutput))
	applyCompleted := false
	for scanner.Scan() {
		groups := rgx.FindStringSubmatch(scanner.Text())
		if groups != nil {
			applyCompleted = true

			ints := []int{}
			for i := 0; i < 3; i++ {
				j, err := strconv.Atoi(groups[i+1])
				if err != nil {
					t.Fatal(err)
				}
				ints = append(ints, j)
			}
			added, changed, destroyed := ints[0], ints[1], ints[2]
			if added > 0 {
				t.Fatal(`Non-zero "added" count on re-apply`)
			}
			if changed > 0 {
				t.Fatal(`Non-zero "changed" count on re-apply`)
			}
			if destroyed > 0 {
				t.Fatal(`Non-zero "destroyed" count on re-apply`)
			}
			break
		}
	}
	if !applyCompleted {
		// Unlikely unless environment is set to a non-English locale, since
		// actual apply errors will normally be caught by terratest.
		t.Fatal(`Did not find expected string; please use "C" locale`)
	}
}

func verifyDestroy(t *testing.T, applyOutput string) {
	// Verify that we see a successful apply.
	// e.g. "Apply complete! Resources: 2 added, 0 changed, 0 destroyed."
	rgx := regexp.MustCompile(`^Destroy complete! Resources: (\d+) destroyed.$`)
	scanner := bufio.NewScanner(strings.NewReader(applyOutput))
	applyCompleted := false
	for scanner.Scan() {
		groups := rgx.FindStringSubmatch(scanner.Text())
		if groups != nil {
			applyCompleted = true
			destroyed, err := strconv.Atoi(groups[1])
			if err != nil {
				t.Fatal(err)
			}
			if destroyed == 0 {
				t.Fatal(`Zero "destroyed" count on destroy`)
			}
			break
		}
	}
	if !applyCompleted {
		// Unlikely unless environment is set to a non-English locale, since
		// actual apply errors will normally be caught by terratest.
		t.Fatal(`Did not find expected string; please use "C" locale`)
	}
}

func writeVersionsTf(providerVersion string) {
	// Note: insecure.  Accepts arbitrary strings.
	template := `terraform {
  required_providers {
    juju = {
      source = "juju/juju"
      version = "%s"
    }
  }
}
`
	rendered := fmt.Sprintf(template, providerVersion)
	outputPath := "versions.tf"
	output, err := os.Create(outputPath)
	if err != nil {
		log.Fatalf("Could not open file %s: %s", outputPath, err)
	}
	output.WriteString(rendered)
}

func cleanup(t *testing.T, options *terraform.Options) {
	t.Log("Cleanup: destroying model (if not already destroyed)")
	t.Log("         * Terraform-level destroy")
	terraform.Destroy(t, options)

	// Verify that everything is really torn down...
	// This doesn't seem ideal (it only works if the destroy is indeed in progress).
	// Is there a better invocation for tracking the destroy case specifically?
	t.Log(`         * "juju wait-for"`)
	cmd := exec.Command("juju", "wait-for", "model", "main")
	cmd.Run() // Ignore the result

	// Remove terraform state file so it doesn't interfere with subsequent runs
	t.Log(`         * Removing terraform.tfstate file`)
	os.Remove("terraform.tfstate")
}
