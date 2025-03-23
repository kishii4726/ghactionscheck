package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/olekukonko/tablewriter"
	"gopkg.in/yaml.v3"
)

var cli struct {
	File string `arg:"" name:"file" help:"Path to GitHub Actions workflow file"`
}

type Workflow struct {
	Jobs        map[string]Job `yaml:"jobs"`
	Defaults    *Defaults      `yaml:"defaults"`
	Concurrency interface{}    `yaml:"concurrency"`
}

type Defaults struct {
	Run *RunDefaults `yaml:"run"`
}

type RunDefaults struct {
	Shell string `yaml:"shell"`
}

type Job struct {
	TimeoutMinutes *int                     `yaml:"timeout-minutes"`
	Permissions    *map[string]string       `yaml:"permissions"`
	Steps          []map[string]interface{} `yaml:"steps"`
	RunsOn         interface{}              `yaml:"runs-on"`
}

type Check struct {
	ID          string `yaml:"id"`
	Description string `yaml:"description"`
	Message     string `yaml:"message"`
	Detail      string `yaml:"detail"`
	Enabled     *bool  `yaml:"enabled,omitempty"`
}

type ChecksConfig struct {
	Checks []Check `yaml:"checks"`
}

type CheckResult struct {
	JobName     string
	Message     string
	Description string
}

var commitHashPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

func loadChecksConfig() (*ChecksConfig, error) {
	data, err := os.ReadFile("checks.yaml")
	if err != nil {
		return nil, fmt.Errorf("error reading checks config: %v", err)
	}

	var config ChecksConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("error parsing checks config: %v", err)
	}

	return &config, nil
}

func findCheck(checks []Check, id string) *Check {
	for _, check := range checks {
		if check.ID == id {
			if check.Enabled == nil || *check.Enabled {
				return &check
			}
			return nil
		}
	}
	return nil
}

func main() {
	ctx := kong.Parse(&cli)
	if ctx.Error != nil {
		fmt.Printf("Error parsing arguments: %v\n", ctx.Error)
		os.Exit(1)
	}

	checksConfig, err := loadChecksConfig()
	if err != nil {
		fmt.Printf("Error loading checks config: %v\n", err)
		os.Exit(1)
	}

	data, err := os.ReadFile(cli.File)
	if err != nil {
		fmt.Printf("Error reading file: %v\n", err)
		os.Exit(1)
	}

	var workflow Workflow
	err = yaml.Unmarshal(data, &workflow)
	if err != nil {
		fmt.Printf("Error parsing YAML: %v\n", err)
		os.Exit(1)
	}

	results := checkWorkflow(workflow, checksConfig.Checks)
	outputResults(results)
}

func checkWorkflow(workflow Workflow, checks []Check) []CheckResult {
	var results []CheckResult

	if workflow.Concurrency == nil {
		check := findCheck(checks, "concurrency")
		if check != nil {
			results = append(results, CheckResult{
				JobName:     "workflow",
				Message:     check.Message,
				Description: check.Detail,
			})
		}
	}

	if workflow.Defaults == nil || workflow.Defaults.Run == nil || workflow.Defaults.Run.Shell == "" {
		check := findCheck(checks, "default_shell")
		if check != nil {
			results = append(results, CheckResult{
				JobName:     "workflow",
				Message:     check.Message,
				Description: check.Detail,
			})
		}
	}

	for jobName, job := range workflow.Jobs {
		if runsOn, ok := job.RunsOn.(string); ok {
			if strings.Contains(runsOn, "latest") {
				check := findCheck(checks, "runner_version")
				results = append(results, CheckResult{
					JobName:     jobName,
					Message:     fmt.Sprintf(check.Message, runsOn),
					Description: check.Detail,
				})
			}
		} else if runsOnList, ok := job.RunsOn.([]interface{}); ok {
			for _, runner := range runsOnList {
				if runnerStr, ok := runner.(string); ok {
					if strings.Contains(runnerStr, "latest") {
						check := findCheck(checks, "runner_version")
						results = append(results, CheckResult{
							JobName:     jobName,
							Message:     fmt.Sprintf(check.Message, runnerStr),
							Description: check.Detail,
						})
					}
				}
			}
		}

		if job.TimeoutMinutes == nil {
			hasStepTimeout := false
			for _, step := range job.Steps {
				if _, ok := step["timeout-minutes"]; ok {
					hasStepTimeout = true
					break
				}
			}

			if !hasStepTimeout {
				check := findCheck(checks, "timeout")
				results = append(results, CheckResult{
					JobName:     jobName,
					Message:     check.Message,
					Description: check.Detail,
				})
			}
		}

		if job.Permissions == nil {
			check := findCheck(checks, "permissions")
			results = append(results, CheckResult{
				JobName:     jobName,
				Message:     check.Message,
				Description: check.Detail,
			})
		} else {
			perms := *job.Permissions
			if perms["contents"] == "write-all" {
				check := findCheck(checks, "unrestricted_permissions")
				results = append(results, CheckResult{
					JobName:     jobName,
					Message:     check.Message,
					Description: check.Detail,
				})
			}
		}

		for _, step := range job.Steps {
			if uses, ok := step["uses"].(string); ok {
				parts := strings.Split(uses, "@")
				if len(parts) == 2 {
					ref := parts[1]
					if !commitHashPattern.MatchString(ref) {
						check := findCheck(checks, "action_ref")
						results = append(results, CheckResult{
							JobName:     jobName,
							Message:     fmt.Sprintf(check.Message, uses),
							Description: check.Detail,
						})
					}
				}

				if uses == "aws-actions/configure-aws-credentials" || strings.HasPrefix(uses, "aws-actions/configure-aws-credentials@") {
					if with, ok := step["with"].(map[string]interface{}); ok {
						if _, hasAccessKeyID := with["aws-access-key-id"]; hasAccessKeyID {
							check := findCheck(checks, "aws_credentials")
							results = append(results, CheckResult{
								JobName:     jobName,
								Message:     check.Message,
								Description: check.Detail,
							})
						}
					}
				}
			}
		}
	}

	return results
}

func outputResults(results []CheckResult) {
	if len(results) == 0 {
		fmt.Println("No issues found!")
		return
	}

	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"Job", "Message", "Description"})
	table.SetBorders(tablewriter.Border{Left: true, Top: true, Right: true, Bottom: true})
	table.SetCenterSeparator("|")
	table.SetRowLine(true)

	for _, result := range results {
		table.Append([]string{
			result.JobName,
			result.Message,
			result.Description,
		})
	}

	table.Render()
}
