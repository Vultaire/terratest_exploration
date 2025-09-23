package test

import (
	"bufio"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gruntwork-io/terratest/modules/terraform"
)

func TestDeploy(t *testing.T) {
	options := terraform.WithDefaultRetryableErrors(t, &terraform.Options{
		TerraformDir: ".",
	})

	// Fallback cleanup
	defer cleanup(t, options)

	// Deploy and verify "terraform apply"
	afterTerraformApply := applyAndVerify(t, options)

	// Wait for things to settle
	queryParam := `--query=forEach(units, unit => unit.life=="alive" && unit.workload-status=="active" && unit.agent-status=="idle")`
	cmd, err, afterApplyWait := waitAfterApply(t, queryParam, afterTerraformApply)

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
	waitAfterDestroy(t, cmd, err, afterTerraformDestroy)
}

func applyAndVerify(t *testing.T, options *terraform.Options) time.Time {
	startTime := time.Now()
	applyOutput := terraform.InitAndApply(t, options)
	afterTerraformApply := time.Now()
	terraformApplyTime := afterTerraformApply.Sub(startTime)
	t.Logf("Initial terraform apply time: %v\n", terraformApplyTime)
	verifyApply(t, applyOutput)
	return afterTerraformApply
}

func waitAfterApply(t *testing.T, queryParam string, afterTerraformApply time.Time) (*exec.Cmd, error, time.Time) {
	cmd := exec.Command("juju", "wait-for", "model", "main", queryParam)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err != nil {
		t.Fatalf("Error waiting for the model to settle")
	}
	afterApplyWait := time.Now()
	afterApplyTime := afterApplyWait.Sub(afterTerraformApply)
	t.Logf("Post-apply time waiting until model settled: %v\n", afterApplyTime)
	return cmd, err, afterApplyWait
}

func destroyAndVerify(t *testing.T, options *terraform.Options, afterApplyWait time.Time) time.Time {
	destroyOutput := terraform.Destroy(t, options)
	afterTerraformDestroy := time.Now()
	terraformDestroyTime := afterTerraformDestroy.Sub(afterApplyWait)
	t.Logf("Terraform destroy time: %v\n", terraformDestroyTime)
	verifyDestroy(t, destroyOutput)
	return afterTerraformDestroy
}

func waitAfterDestroy(t *testing.T, cmd *exec.Cmd, err error, afterTerraformDestroy time.Time) {
	cmd = exec.Command("juju", "wait-for", "model", "main")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if err != nil {
		t.Fatal("Error waiting for the model to settle")
	}
	afterDestroyWait := time.Now()
	afterDestroyTime := afterDestroyWait.Sub(afterTerraformDestroy)
	t.Logf("Post-apply time waiting until model settled: %v\n", afterDestroyTime)
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
}
