package commands

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// DisplayResults prints the summary, failed checks, and skipped checks.
func (r *TestRunner) DisplayResults(results *TestResults) {
	r.printf("\n=== Test Results ===\n")

	if results.Output != "" {
		r.printf("%s\n", results.Output)
	}

	r.printf("Suite: %s\n", results.Suite)
	r.printf("Duration: %s\n", results.Duration.Round(time.Millisecond))
	r.printf("Tests: %d\n", len(results.Tests))
	r.printf("Passed: %d\n", results.Passed)
	r.printf("Failed: %d\n", results.Failed)
	r.printf("Skipped: %d\n", results.Skipped)

	r.displayByStatus(results, TestStatusFailed, "=== Failed Tests ===")
	r.displayByStatus(results, TestStatusSkipped, "=== Skipped Tests ===")
}

// displayByStatus lists the checks with the given status under a header.
func (r *TestRunner) displayByStatus(results *TestResults, status TestStatus, header string) {
	printed := false

	for _, test := range results.Tests {
		if test.Status != status {
			continue
		}

		if !printed {
			r.printf("\n%s\n", header)

			printed = true
		}

		detail := test.Reason
		if status == TestStatusFailed {
			detail = test.Error
		}

		r.printf("- %s: %s\n", test.Name, detail)
	}
}

// junitTestSuites is the root of a JUnit XML report.
type junitTestSuites struct {
	XMLName  xml.Name         `xml:"testsuites"`
	Name     string           `xml:"name,attr"`
	Tests    int              `xml:"tests,attr"`
	Failures int              `xml:"failures,attr"`
	Skipped  int              `xml:"skipped,attr"`
	Time     string           `xml:"time,attr"`
	Suites   []junitTestSuite `xml:"testsuite"`
}

// junitTestSuite groups the checks of one suite.
type junitTestSuite struct {
	Name      string          `xml:"name,attr"`
	Tests     int             `xml:"tests,attr"`
	Failures  int             `xml:"failures,attr"`
	Skipped   int             `xml:"skipped,attr"`
	Time      string          `xml:"time,attr"`
	Timestamp string          `xml:"timestamp,attr"`
	Cases     []junitTestCase `xml:"testcase"`
}

// junitTestCase is one check.
type junitTestCase struct {
	Name      string        `xml:"name,attr"`
	Classname string        `xml:"classname,attr"`
	Time      string        `xml:"time,attr"`
	Failure   *junitMessage `xml:"failure,omitempty"`
	Skipped   *junitMessage `xml:"skipped,omitempty"`
	SystemOut string        `xml:"system-out,omitempty"`
}

// junitMessage carries a failure or skip message.
type junitMessage struct {
	Message string `xml:"message,attr"`
	Body    string `xml:",chardata"`
}

// SaveResults writes the results as JUnit XML, or as JSON when filename ends
// in .json.
func (r *TestRunner) SaveResults(results *TestResults, filename string) error {
	var (
		content []byte
		err     error
	)

	if strings.HasSuffix(strings.ToLower(filename), ".json") {
		content, err = renderJSONResults(results)
	} else {
		content, err = renderJUnitResults(results)
	}

	if err != nil {
		return err
	}

	err = os.WriteFile(filename, content, TestResultsFileMode)
	if err != nil {
		return fmt.Errorf("failed to write test results to file: %w", err)
	}

	return nil
}

// jsonResults is the JSON report shape.
type jsonResults struct {
	Suite     TestSuite    `json:"suite"`
	Passed    int          `json:"passed"`
	Failed    int          `json:"failed"`
	Skipped   int          `json:"skipped"`
	Duration  string       `json:"duration"`
	StartTime time.Time    `json:"start_time"`
	Note      string       `json:"note,omitempty"`
	Tests     []jsonResult `json:"tests"`
}

// jsonResult is one check in the JSON report.
type jsonResult struct {
	Name     string     `json:"name"`
	Status   TestStatus `json:"status"`
	Duration string     `json:"duration"`
	Error    string     `json:"error,omitempty"`
	Reason   string     `json:"reason,omitempty"`
	Output   string     `json:"output,omitempty"`
	Retries  int        `json:"retries"`
}

// renderJSONResults encodes the results as indented JSON.
func renderJSONResults(results *TestResults) ([]byte, error) {
	report := jsonResults{
		Suite:     results.Suite,
		Passed:    results.Passed,
		Failed:    results.Failed,
		Skipped:   results.Skipped,
		Duration:  results.Duration.String(),
		StartTime: results.StartTime,
		Note:      results.Output,
		Tests:     make([]jsonResult, 0, len(results.Tests)),
	}

	for _, t := range results.Tests {
		report.Tests = append(report.Tests, jsonResult{
			Name:     t.Name,
			Status:   t.Status,
			Duration: t.Duration.String(),
			Error:    t.Error,
			Reason:   t.Reason,
			Output:   t.Output,
			Retries:  t.Retries,
		})
	}

	out, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to encode results: %w", err)
	}

	return append(out, '\n'), nil
}

// renderJUnitResults encodes the results as JUnit XML, grouping checks into
// testsuite elements by the suite prefix of their names.
func renderJUnitResults(results *TestResults) ([]byte, error) {
	root := junitTestSuites{
		XMLName:  xml.Name{Space: "", Local: "testsuites"},
		Name:     "ocfp test " + string(results.Suite),
		Tests:    len(results.Tests),
		Failures: results.Failed,
		Skipped:  results.Skipped,
		Time:     junitSeconds(results.Duration),
		Suites:   nil,
	}

	index := map[string]int{}

	for _, t := range results.Tests {
		suiteName, checkName := splitStepName(t.Name)

		i, ok := index[suiteName]
		if !ok {
			i = len(root.Suites)
			index[suiteName] = i

			root.Suites = append(root.Suites, junitTestSuite{
				Name:      suiteName,
				Tests:     0,
				Failures:  0,
				Skipped:   0,
				Time:      "0",
				Timestamp: results.StartTime.UTC().Format(time.RFC3339),
				Cases:     nil,
			})
		}

		suite := &root.Suites[i]
		suite.Tests++
		suite.Cases = append(suite.Cases, junitCase(t, suiteName, checkName))

		switch t.Status {
		case TestStatusFailed:
			suite.Failures++
		case TestStatusSkipped:
			suite.Skipped++
		case TestStatusPassed, TestStatusRunning:
		}
	}

	for i := range root.Suites {
		var total time.Duration

		for _, c := range root.Suites[i].Cases {
			seconds, _ := strconv.ParseFloat(c.Time, 64)
			total += time.Duration(seconds * float64(time.Second))
		}

		root.Suites[i].Time = junitSeconds(total)
	}

	out, err := xml.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to encode results: %w", err)
	}

	return append([]byte(xml.Header), append(out, '\n')...), nil
}

// junitCase converts one result into a testcase element.
func junitCase(t TestResult, suiteName, checkName string) junitTestCase {
	c := junitTestCase{
		Name:      checkName,
		Classname: "ocfp.test." + suiteName,
		Time:      junitSeconds(t.Duration),
		Failure:   nil,
		Skipped:   nil,
		SystemOut: t.Output,
	}

	switch t.Status {
	case TestStatusFailed:
		c.Failure = &junitMessage{Message: firstLine(t.Error), Body: t.Error}
	case TestStatusSkipped:
		c.Skipped = &junitMessage{Message: t.Reason, Body: ""}
	case TestStatusPassed, TestStatusRunning:
	}

	return c
}

// splitStepName splits <suite>/<check>; a name without a slash is its own
// suite.
func splitStepName(name string) (string, string) {
	suite, check, ok := strings.Cut(name, "/")
	if !ok {
		return name, name
	}

	return suite, check
}

// junitSeconds formats a duration as fractional seconds.
func junitSeconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', 3, 64)
}
