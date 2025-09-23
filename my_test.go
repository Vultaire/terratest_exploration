package test

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/user"
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

	// Fallback cleanup
	defer cleanup(t, options)

	// Deploy and verify "terraform apply"
	afterTerraformApply := applyAndVerify(t, options)

	// Wait for things to settle
	queryParam := `--query=forEach(units, unit => unit.life=="alive" && unit.workload-status=="active" && unit.agent-status=="idle")`
	afterApplyWait := waitAfterApply(t, queryParam, afterTerraformApply)

	/*
		To do later: run verification check re: what was actually deployed.
		Maybe a "juju export-bundle" comparison tool (versus an expected state, with
		certain fields being ignored) might be a way to accomplish this.
	*/

	// Tear down and verify "terraform destroy"
	afterTerraformDestroy := destroyAndVerify(t, options, afterApplyWait)

	// Verify that everything is really torn down...
	// This doesn't seem ideal (it only works if the destroy is indeed in progress).
	// Is there a better invocation for tracking the destroy case specifically?
	waitAfterDestroy(t, afterTerraformDestroy)
}

func TestApplyUpgradeAndDestroy(t *testing.T) {
	// The procedure for testing with the non-released version is pretty hacky;
	// the current way to enable this is to build it as a drop-in replacement
	// for an officially released, and then blow everything away after the test
	// is done.
	options := terraform.WithDefaultRetryableErrors(t, &terraform.Options{
		TerraformDir: ".",
	})

	defer cleanupUpgradeTest(t, options)

	/* versions.tf modifications:
	* Backup to versions.tf.bak.original.
	* Update to initial deploy version.
	* Apply.
	* Backup to versions.tf.bak.applied.
	* Update to upgraded version.
	* Re-apply and verify no updates.
	* Destroy.
	* Restore versions.tf.bak.applied.
	* Fallback destroy.
	* Restore versions.tf.bak.original
	 */

	deployVersion := "0.22.0"
	upgradeVersion := "v1.0.0-beta2"

	versionsTf := "versions.tf"

	// Backup and queue restore of versions.tf
	versionsTfOriginalBackup := "versions.tf.bak.original"
	copyFile(versionsTf, versionsTfOriginalBackup)
	defer copyFile(versionsTfOriginalBackup, versionsTf)
	writeVersionsTf(versionsTf, deployVersion)

	// Deploy and verify "terraform apply"
	afterTerraformApply := applyAndVerify(t, options)

	// Wait for things to settle
	queryParam := `--query=forEach(units, unit => unit.life=="alive" && unit.workload-status=="active" && unit.agent-status=="idle")`
	afterApplyWait := waitAfterApply(t, queryParam, afterTerraformApply)

	// // Update provider version
	// versionsTfAppliedBackup := "versions.tf.bak.applied"
	// copyFile(versionsTf, versionsTfAppliedBackup)
	// defer copyFile(versionsTfOriginalBackup, versionsTf)
	// writeVersionsTf(versionsTf, upgradeVersion)

	// Build custom provider
	err := buildCustomProvider(t, upgradeVersion, deployVersion)
	if err != nil {
		t.Fatalf("Unable to build custom provider version %s: %s", upgradeVersion, err)
	}

	// Re-apply and verify no changes
	afterTerraformReApply := applyAndReVerify(t, options, afterApplyWait)

	/*
		To do later: run verification check re: what was actually deployed.
		Maybe a "juju export-bundle" comparison tool (versus an expected state, with
		certain fields being ignored) might be a way to accomplish this.
	*/

	// Tear down and verify "terraform destroy"
	afterTerraformDestroy := destroyAndVerify(t, options, afterTerraformReApply)

	// Verify that everything is really torn down...
	// This doesn't seem ideal (it only works if the destroy is indeed in progress).
	// Is there a better invocation for tracking the destroy case specifically?
	waitAfterDestroy(t, afterTerraformDestroy)
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
	cmd := exec.Command("juju", "wait-for", "model", "main", queryParam)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err != nil {
		t.Fatalf("Error waiting for the model to settle")
	}
	timestamp := time.Now()
	duration := timestamp.Sub(lastTimestamp)
	t.Logf("Post-apply time waiting until model settled: %v\n", duration)
	return timestamp
}

// func TestBuildProvider(t *testing.T) {
// 	buildCustomProvider(t, "v1.0.0-beta2", "0.22.0")
// }

func buildCustomProvider(t *testing.T, version string, currentVersion string) error {
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
		cmd := exec.Command("which", executable)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		if err != nil {
			t.Fatalf("Did not find prerequisite executable: %s", executable)
		}
	}

	t.Log("Creating temp dir")
	dname, err := os.MkdirTemp("", "builddir")
	if err != nil {
		t.Fatalf("Error setting up tempdir for building custom provider: %s", err)
	}
	defer os.RemoveAll(dname)

	t.Log("Performing shallow clone of provider repo")
	cmd := exec.Command("git", "clone", "--depth", "1", "--branch", version,
		"https://github.com/juju/terraform-provider-juju.git")
	cmd.Dir = dname
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if err != nil {
		t.Fatalf("Error cloning terraform-provider-juju repo: %s", err)
	}

	t.Log("Building the provider")
	makeCommands := [][]string{
		{"make", "install-dependencies"},
		{"make", "go-install"}, /* builds into gopath */
		{"make", "install", fmt.Sprintf("EDGE_VERSION=%s", currentVersion)}, /* replaces existing provider */
	}
	for _, makeCommand := range makeCommands {
		t.Logf("Running command: %v", makeCommand)
		cmd = exec.Command(makeCommand[0], makeCommand[1:]...) // Will this work sanely on our builders?
		cmd.Dir = filepath.Join(dname, "terraform-provider-juju")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err = cmd.Run()
		if err != nil {
			t.Fatalf("Error building terraform-provider-juju repo: %s", err)
		}
	}

	t.Log("Removing the old .terraform.lock.hcl file")
	os.Remove(".terraform.lock.hcl")

	return err
}

func cleanupUpgradeTest(t *testing.T, options *terraform.Options) {
	// Remove the lock file; it may have references to the custom-built provider, which we don't want to keep.
	t.Logf("Cleanup: removing Terraform lock file")
	os.Remove(".terraform.lock.hcl")

	// t.Logf("Cleanup: removing terraform.tfstate (if not already purged)")
	// os.Remove("terraform.tfstate")

	t.Logf("Cleanup: removing .terraform (if not already purged)")
	os.RemoveAll(".terraform")

	// Remove the juju provider; we want to make sure we set it up from scratch on the next build.
	user, err := user.Current()
	if err != nil {
		t.Fatalf("Could not pull current user: %s", err)
	}
	providerDir := filepath.Join(user.HomeDir, ".terraform.d", "plugins", "registry.terraform.io", "juju", "juju")
	t.Logf("Cleanup: purging Terraform juju provider from ~/.terraform.d/plugins/")
	os.RemoveAll(providerDir)

	// Re-run terraform init just for the sake of running cleanup
	terraform.Init(t, options)

	// Fallback to the normal cleanup code
	cleanup(t, options)

	/*
		To do: use **THIS METHOD** instead for building/running a development provider:
		https://developer.hashicorp.com/terraform/cli/config/config-file#provider-installation
	*/
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
	cmd := exec.Command("juju", "wait-for", "model", "main")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err != nil {
		t.Fatal("Error waiting for the model to settle")
	}
	timestamp := time.Now()
	duration := timestamp.Sub(lastTimestamp)
	t.Logf("Post-apply time waiting until model settled: %v\n", duration)
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

func copyFile(source string, dest string) {
	input, err := os.Open(source)
	if err != nil {
		log.Fatalf("Could not open source %s: %s", source, err)
	}
	output, err := os.Create(dest)
	if err != nil {
		log.Fatalf("Could not open destination %s: %s", dest, err)
	}
	_, err = io.Copy(output, input)
	if err != nil {
		log.Fatalf("Unexpected error on io.Copy: %s", err)
	}
}

func writeVersionsTf(path string, providerVersion string) {
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
	output, err := os.Create(path)
	if err != nil {
		log.Fatalf("Could not open file %s: %s", path, err)
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
