# Rerank Two-Stage Bypass Design

## Goal

Reduce keyword-driven rerank mistakes by separating semantic extraction from role classification.

The bypass test is for prompt and rule validation only. It does not change the production retrieval path.

## Stage 1: Evidence Semantic Extraction

Input:

- Parsed question fields: question, subject, intent, audience, constraint.
- Candidate evidence grouped by source document.

Output per candidate:

- `source_theme`: theme implied by the source document.
- `content_theme`: theme of the candidate text.
- `intent`: what information the evidence provides.
- `object`: the people, roles, teams, or entities targeted by the evidence.
- `scope`: product, scenario, time, business process, or applicability constraints.
- `key_facts`: concrete facts extracted from the evidence. (Retired in V1 — the
  production judge now reads the KU's knowledge points instead; see
  `docs/impl/v1/semantics-curation.md`.)

Stage 1 must not output relevance decisions or relationship labels. It must not use `direct`, `supporting`, `irrelevant`, `related`, `conflict`, or similar relationship conclusions.

## Stage 2: Rule-Based Relationship Classification

Input:

- Parsed question fields.
- Stage 1 extraction output only.

Rules:

1. Theme hard gate:
   If the question theme is project implementation incentive and the evidence theme is sales, marketing, channel, signing, or sales reward policy, classify as `irrelevant`.

2. Object hard gate:
   If the question object is project manager / implementation staff and the evidence object is sales, marketing center, signing person, promoter, channel partner, or agent, classify as `irrelevant`.

3. Scope hard gate:
   If the question scope is implementation / delivery / project bonus and the evidence scope is sales promotion / contract signing / marketing reward, classify as `irrelevant`.

4. Direct:
   If no hard gate fails and the evidence provides the core amount, formula, coefficient, or rule needed to answer the question, classify as `direct`.

5. Supporting:
   If no hard gate fails and the evidence provides calculation steps, constraints, payout timing, performance coefficients, or required background, classify as `supporting`.

6. Fallback:
   Otherwise classify as `irrelevant`.

## Current Test Case

Question:

`实施万相公文可以拿到多少奖金？`

Expected:

- All candidates from `万相公文销售奖励制度` are `irrelevant`.
- `项目考核与激励制度` 5.1 with the 万相公文 1% coefficient is `direct`.
- Project bonus calculation, payout, performance coefficient, and loss constraints are `supporting`.
- Responsibility-only process text is `irrelevant`.
