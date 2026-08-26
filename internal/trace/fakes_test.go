package trace

// fakeSynonymEnricher implements ObservedConditionEnricher for testing
// Trace's confidence-wiring behavior in isolation from the real activation
// package.
type fakeSynonymEnricher struct {
	enrichCalls int

	recordOutcomeCalls      []recordOutcomeCall
	recordAuditOutcomeCalls []recordAuditOutcomeCall
	recordOutcomeErr        error
	recordAuditOutcomeErr   error

	recordBundleOutcomeCalls []recordBundleOutcomeCall
	recordMemberOutcomeCalls []recordMemberOutcomeCall
	recordBundleOutcomeErr   error
	recordMemberOutcomeErr   error
}

// recordBundleOutcomeCall/recordMemberOutcomeCall mirror recordOutcomeCall
// for ActivationBundle's two axes (docs/impl/v1/activation-bundle.md「验证」
// 阶段 2 接线).
type recordBundleOutcomeCall struct {
	bundleID, subject, intent, audience, constraint string
	success                                         bool
}

type recordMemberOutcomeCall struct {
	bundleID, pointID string
	success           bool
}

// recordOutcomeCall/recordAuditOutcomeCall capture every RecordOutcome /
// RecordAuditOutcome invocation the fake enricher receives, so tests can
// assert on exact call count and arguments (docs/impl/v1/trace.md 完成标准).
type recordOutcomeCall struct {
	linkID, subject, intent, audience, constraint string
	success                                       bool
	questionTerms                                 string
}

type recordAuditOutcomeCall struct {
	linkID, subject, intent, audience, constraint string
	agree                                         bool
}

func (f *fakeSynonymEnricher) EnrichFromConfidentFullPath(pointIDs []string, subject, intent, audience, constraint, questionTerms string, max int) error {
	f.enrichCalls++
	return nil
}

func (f *fakeSynonymEnricher) RecordOutcome(linkID, subject, intent, audience, constraint string, success bool, questionTerms string) error {
	f.recordOutcomeCalls = append(f.recordOutcomeCalls, recordOutcomeCall{linkID, subject, intent, audience, constraint, success, questionTerms})
	return f.recordOutcomeErr
}

func (f *fakeSynonymEnricher) RecordAuditOutcome(linkID, subject, intent, audience, constraint string, agree bool) error {
	f.recordAuditOutcomeCalls = append(f.recordAuditOutcomeCalls, recordAuditOutcomeCall{linkID, subject, intent, audience, constraint, agree})
	return f.recordAuditOutcomeErr
}

func (f *fakeSynonymEnricher) RecordBundleOutcome(bundleID, subject, intent, audience, constraint string, success bool) error {
	f.recordBundleOutcomeCalls = append(f.recordBundleOutcomeCalls, recordBundleOutcomeCall{bundleID, subject, intent, audience, constraint, success})
	return f.recordBundleOutcomeErr
}

func (f *fakeSynonymEnricher) RecordMemberOutcome(bundleID, pointID string, success bool) error {
	f.recordMemberOutcomeCalls = append(f.recordMemberOutcomeCalls, recordMemberOutcomeCall{bundleID, pointID, success})
	return f.recordMemberOutcomeErr
}
