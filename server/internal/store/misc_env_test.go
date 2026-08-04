package store

import (
	"os"
	osexec "os/exec"
	"strings"
	"testing"
)

const miscEnvTestScenario = "AIVORY_TEST_STORE_MISC_ENV_SCENARIO"

var miscEnvKeys = []string{
	"AIVORY_STORE_M_CONFIDENCE",
	"AIVORY_STORE_LIST_MEMORIES_ACTIVE",
	"AIVORY_STORE_ADMIN_USAGE_RECORDS_LIMIT",
	"AIVORY_STORE_ADMIN_USAGE_RECORDS_LIMIT_2",
	"AIVORY_STORE_USAGE_TREND_WINDOW",
	"AIVORY_STORE_USAGE_TREND_HOURLY_BUCKET_THRESHOLD",
	"AIVORY_STORE_USAGE_TOTALS_WINDOW",
	"AIVORY_STORE_USAGE_BREAKDOWN_TOP_N",
	"AIVORY_STORE_USAGE_BREAKDOWN_WINDOW",
	"AIVORY_STORE_USAGE_SERIES_WINDOW",
}

type miscEnvValues struct {
	memoryConfidence         float64
	activeMemories           int
	usageRecordsLimit        int
	usageRecordsDefaultLimit int
	trendWindow              int
	hourlyBucketThreshold    int
	totalsWindow             int
	breakdownTopN            int
	breakdownWindow          int
	seriesWindow             int
}

func TestMiscEnvironmentDefaultsAndOverrides(t *testing.T) {
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}

	tests := []struct {
		name      string
		scenario  string
		overrides map[string]string
	}{
		{name: "defaults", scenario: "defaults"},
		{
			name:     "overrides",
			scenario: "overrides",
			overrides: map[string]string{
				"AIVORY_STORE_M_CONFIDENCE":                        "0.65",
				"AIVORY_STORE_LIST_MEMORIES_ACTIVE":                "31",
				"AIVORY_STORE_ADMIN_USAGE_RECORDS_LIMIT":           "401",
				"AIVORY_STORE_ADMIN_USAGE_RECORDS_LIMIT_2":         "37",
				"AIVORY_STORE_USAGE_TREND_WINDOW":                  "11",
				"AIVORY_STORE_USAGE_TREND_HOURLY_BUCKET_THRESHOLD": "3",
				"AIVORY_STORE_USAGE_TOTALS_WINDOW":                 "13",
				"AIVORY_STORE_USAGE_BREAKDOWN_TOP_N":               "17",
				"AIVORY_STORE_USAGE_BREAKDOWN_WINDOW":              "19",
				"AIVORY_STORE_USAGE_SERIES_WINDOW":                 "23",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cmd := osexec.Command(testBinary, "-test.run=^TestMiscEnvironmentHelperProcess$")
			cmd.Env = miscChildEnvironment(test.scenario, test.overrides)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("helper process failed: %v\n%s", err, output)
			}
		})
	}
}

func TestMiscEnvironmentHelperProcess(t *testing.T) {
	scenario := os.Getenv(miscEnvTestScenario)
	if scenario == "" {
		return
	}

	got := miscEnvironmentValues()
	var want miscEnvValues
	switch scenario {
	case "defaults":
		want = miscEnvValues{0.8, 20, 500, 50, 7, 2, 7, 8, 7, 7}
	case "overrides":
		want = miscEnvValues{0.65, 31, 401, 37, 11, 3, 13, 17, 19, 23}
	default:
		t.Fatalf("unknown helper scenario %q", scenario)
	}
	if got != want {
		t.Fatalf("misc environment values = %+v, want %+v", got, want)
	}
}

func miscEnvironmentValues() miscEnvValues {
	return miscEnvValues{
		memoryConfidence:         mConfidence,
		activeMemories:           listMemoriesActive,
		usageRecordsLimit:        adminUsageRecordsLimit,
		usageRecordsDefaultLimit: adminUsageRecordsLimit2,
		trendWindow:              usageTrendWindow,
		hourlyBucketThreshold:    usageTrendHourlyBucketThreshold,
		totalsWindow:             usageTotalsWindow,
		breakdownTopN:            usageBreakdownTopN,
		breakdownWindow:          usageBreakdownWindow,
		seriesWindow:             usageSeriesWindow,
	}
}

func miscChildEnvironment(scenario string, overrides map[string]string) []string {
	blocked := make(map[string]struct{}, len(miscEnvKeys)+1)
	blocked[miscEnvTestScenario] = struct{}{}
	for _, key := range miscEnvKeys {
		blocked[key] = struct{}{}
	}

	env := make([]string, 0, len(os.Environ())+len(overrides)+1)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, skip := blocked[key]; !skip {
			env = append(env, entry)
		}
	}
	env = append(env, miscEnvTestScenario+"="+scenario)
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}
