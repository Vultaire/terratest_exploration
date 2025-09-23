package test

import (
	"bufio"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/gruntwork-io/terratest/modules/terraform"
)

func TestDeploy(t *testing.T) {
	options := terraform.WithDefaultRetryableErrors(t, &terraform.Options{
		TerraformDir: ".",
	})

	// Fallback cleanup
	defer terraform.Destroy(t, options)

	// Deploy and verify "terraform apply"
	apply_output := terraform.InitAndApply(t, options)
	verifyApply(t, apply_output)

	// Wait for things to settle
	cmd := exec.Command("juju", "wait-for", "model", "main",
		`--query=forEach(units, unit => unit.life=="alive" && unit.workload-status=="active" && unit.agent-status=="idle")`)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err != nil {
		t.Fatalf("Error waiting for the model to settle")
	}

	// // WIP: Verify some stuff about the deployed environment.
	// // (Do we care right now?)
	// var v any
	// if err := json.NewDecoder(strings.NewReader(stdout.String())).Decode(&v); err != nil {
	// 	t.Fatalf("JSON decode error: %v\n", stderr.String())
	// }
	// log.Printf("%#v\n", v)

	// * Are all the things present as requested?  -> would need to know what's been requested.
	// * Are all units in active/idle state?
	//   * If not, are there known exceptions or things being ignored?
	//   * If still not: repeat for N seconds until timeout hit?
	//
	/*
		At JSON level:
		* applications.ubuntu."application-status".current: active
		* applications.ubuntu.units."ubuntu/0"."juju-status".current: idle
		* applications.ubuntu.units."ubuntu/0"."workload-status".current: active
	*/
	//
	// For v1.0+: it should be in a good state once terraform returns.
	// For earlier: it'll kick things off but won't wait.

	/*
		Long story short: how deep of verification do we need here?
		* Immediately *requested* tests: deploy, maybe upgrade, verify that it doesn't break

	*/

	// Tear down and verify "terraform destroy"
	destroy_output := terraform.Destroy(t, options)
	verifyDestroy(t, destroy_output)

	// Verify that everything is really torn down...
	// This doesn't seem ideal (it only works if the destroy is indeed in progress).
	// Is there a better invocation for tracking the destroy case specifically?
	cmd = exec.Command("juju", "wait-for", "model", "main")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if err != nil {
		t.Fatal("Error waiting for the model to settle")
	}
}

func verifyApply(t *testing.T, apply_output string) {
	// Verify that we see a successful apply.
	// e.g. "Apply complete! Resources: 2 added, 0 changed, 0 destroyed."
	rgx := regexp.MustCompile(`^Apply complete! Resources: (\d+) added, (\d+) changed, (\d+) destroyed.$`)
	scanner := bufio.NewScanner(strings.NewReader(apply_output))
	apply_completed := false
	for scanner.Scan() {
		groups := rgx.FindStringSubmatch(scanner.Text())
		if groups != nil {
			apply_completed = true

			ints := make([]int, 3)
			for i := 0; i < 3; i++ {
				j, err := strconv.Atoi(groups[i+1])
				if err != nil {
					t.Fatal(err)
				}
				ints[i] = j
			}
			added, changed, destroyed := ints[0], ints[1], ints[2]
			if added == 0 {
				t.Fatal(`Unexpected zero "added" count`)
			}
			if changed > 0 {
				t.Fatal(`Non-zero "changed" count on initial apply`)
			}
			if destroyed > 0 {
				t.Fatal(`Non-zero "destroyed" count on initial apply`)
			}
			break
		}
	}
	if !apply_completed {
		// Unlikely unless environment is set to a non-English locale, since
		// actual apply errors will normally be caught by terratest.
		t.Fatal(`Did not find expected string; please use "C" locale`)
	}
	// fmt.Println("======================================================================")
	// fmt.Println(apply_output)
	// fmt.Println("======================================================================")
}

func verifyDestroy(t *testing.T, apply_output string) {
	return
	// Verify that we see a successful apply.
	// e.g. "Apply complete! Resources: 2 added, 0 changed, 0 destroyed."
	rgx := regexp.MustCompile(`^Destroy complete! Resources: (\d+) destroyed.$`)
	scanner := bufio.NewScanner(strings.NewReader(apply_output))
	apply_completed := false
	for scanner.Scan() {
		groups := rgx.FindStringSubmatch(scanner.Text())
		if groups != nil {
			apply_completed = true

			ints := make([]int, 3)
			for i := 0; i < 3; i++ {
				j, err := strconv.Atoi(groups[i+1])
				if err != nil {
					t.Fatal(err)
				}
				ints[i] = j
			}
			added, changed, destroyed := ints[0], ints[1], ints[2]
			if added == 0 {
				t.Fatal(`Unexpected zero "added" count`)
			}
			if changed > 0 {
				t.Fatal(`Non-zero "changed" count on initial apply`)
			}
			if destroyed > 0 {
				t.Fatal(`Non-zero "destroyed" count on initial apply`)
			}
			break
		}
	}
	if !apply_completed {
		// Unlikely unless environment is set to a non-English locale, since
		// actual apply errors will normally be caught by terratest.
		t.Fatal(`Did not find expected string; please use "C" locale`)
	}
	// fmt.Println("======================================================================")
	// fmt.Println(apply_output)
	// fmt.Println("======================================================================")
}
