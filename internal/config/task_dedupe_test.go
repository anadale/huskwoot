package config

import "testing"

func TestLoad_TaskDuplicateThresholds(t *testing.T) {
	cases := []struct {
		name      string
		settings  string
		duplicate float64
		review    float64
		wantError bool
	}{
		{name: "custom thresholds", settings: "duplicate_threshold = 0.92\nreview_threshold = 0.65", duplicate: 0.92, review: 0.65},
		{name: "upper boundary", settings: "duplicate_threshold = 1.0\nreview_threshold = 0.5", duplicate: 1, review: 0.5},
		{name: "equal boundaries", settings: "duplicate_threshold = 0.8\nreview_threshold = 0.8", wantError: true},
		{name: "inverted boundaries", settings: "duplicate_threshold = 0.6\nreview_threshold = 0.9", wantError: true},
		{name: "zero duplicate boundary", settings: "duplicate_threshold = 0.0", wantError: true},
		{name: "zero review boundary", settings: "review_threshold = 0.0", wantError: true},
		{name: "negative review boundary", settings: "review_threshold = -0.1", wantError: true},
		{name: "duplicate above one", settings: "duplicate_threshold = 1.01", wantError: true},
		{name: "nan duplicate", settings: "duplicate_threshold = nan", wantError: true},
		{name: "infinite review", settings: "review_threshold = inf", wantError: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(writeConfig(t, validBase()+"\n[tasks]\n"+tc.settings+"\n"))
			if tc.wantError {
				if err == nil {
					t.Fatal("unsafe duplicate thresholds were accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Tasks.DuplicateThreshold != tc.duplicate || cfg.Tasks.ReviewThreshold != tc.review {
				t.Fatalf("thresholds = %g/%g, want %g/%g", cfg.Tasks.DuplicateThreshold, cfg.Tasks.ReviewThreshold, tc.duplicate, tc.review)
			}
		})
	}
}
