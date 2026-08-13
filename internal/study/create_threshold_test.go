package study

import (
	"testing"
)

// TestScanCandidates_BetaGate_ReplacesRawRatio verifies the 2026-08-13
// creation-threshold rewrite (docs/impl/v1/study.md 步骤 1,
// docs/design/activation-convergence.md 第 11 节): the raw confident-count/
// ratio gate is replaced by mean_pre/width_pre Laplace-smoothed Beta
// estimates. A single observation with a 100% raw ratio (s=1,h=1) must NOT
// qualify a create_confidence_min≈0.55-0.6 gate combined with a realistic
// width_pre gate — mean_pre=(1+1)/(1+2)=0.667 clears a 0.55 confidence
// floor on its own, but width_pre=0.667*0.333/4≈0.056 is far too wide at
// n=1 to be considered "converged" (compare docs/impl/v1/study.md's
// create_width_max default of 0.03) — this is exactly the small-sample
// false-positive the raw ratio gate could not catch (a raw ratio would have
// wrongly qualified 1/1 as 100% confident).
func TestScanCandidates_BetaGate_SingleObservation_DoesNotQualify(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	seedSource(t, db, "src1")
	seedDomain(t, db, "dom1", "D")
	seedEntry(t, db, "con1", "dom1", "C")
	seedKU(t, db, "ku1", "src1", "con1")
	seedKP(t, db, "kp1", "ku1", "src1", "content")

	// s=1, h=1: raw ratio 1/1 = 100%; mean_pre=(1+1)/(1+2)=0.667, width_pre=
	// 0.667*0.333/(1+3)≈0.0556.
	seedCooccurrence(t, db, "t1", "kp1", 1, 1)

	count, err := store.ScanCandidates(0.55, 0.03, 200)
	if err != nil {
		t.Fatalf("ScanCandidates: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected single observation (s=1,h=1) to NOT qualify create_width_max=0.03, got count=%d", count)
	}
}

// TestScanCandidates_BetaGate_Boundaries exercises a few representative
// (confident, hit) pairs against the create_confidence_min/create_width_max
// gate, confirming both the confidence floor and the width ceiling are
// enforced independently.
func TestScanCandidates_BetaGate_Boundaries(t *testing.T) {
	cases := []struct {
		name           string
		confident, hit int
		wantQualifies  bool
	}{
		// mean_pre=(6+1)/(8+2)=0.70 >= 0.55; width_pre=0.70*0.30/11≈0.0191 <= 0.03
		{"clears both gates", 6, 8, true},
		// mean_pre=(3+1)/(5+2)≈0.571 >= 0.55; width_pre≈0.031 > 0.03 (too wide)
		{"clears confidence, too wide", 3, 5, false},
		// mean_pre=(2+1)/(10+2)=0.25 < 0.55 (low confidence, plenty of samples)
		{"below confidence floor", 2, 10, false},
		// mean_pre=(0+1)/(0+2)=0.5 < 0.55 (no observations at all)
		{"zero observations", 0, 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			store := NewStore(db)
			seedSource(t, db, "src1")
			seedDomain(t, db, "dom1", "D")
			seedEntry(t, db, "con1", "dom1", "C")
			seedKU(t, db, "ku1", "src1", "con1")
			seedKP(t, db, "kp1", "ku1", "src1", "content")
			if tc.hit > 0 {
				seedCooccurrence(t, db, "t1", "kp1", tc.hit, tc.confident)
			}

			count, err := store.ScanCandidates(0.55, 0.03, 200)
			if err != nil {
				t.Fatalf("ScanCandidates: %v", err)
			}
			gotQualifies := count == 1
			if gotQualifies != tc.wantQualifies {
				t.Errorf("confident=%d hit=%d: qualifies=%v, want %v", tc.confident, tc.hit, gotQualifies, tc.wantQualifies)
			}
		})
	}
}
