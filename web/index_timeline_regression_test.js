const fs = require('node:fs');
const path = require('node:path');

const page = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');

function requirePattern(pattern, message) {
  if (!pattern.test(page)) {
    throw new Error(message);
  }
}

requirePattern(/sourceTimelineUploadGeneration:\s*0/, 'upload timeline generation state is required');
requirePattern(/sourceTimelineRequestGeneration:\s*0/, 'persisted timeline request generation state is required');
requirePattern(/var uploadGeneration = \+\+S\.sourceTimelineUploadGeneration;/, 'new uploads must claim timeline ownership');
requirePattern(/if \(uploadGeneration !== S\.sourceTimelineUploadGeneration\) return;/, 'stale upload responses must not render');
requirePattern(/pollSourceTimeline\(r\.data\.source_id, file\.name, uploadStart, uploadGeneration\)/, 'polls must retain upload ownership generation');
requirePattern(/async function pollSourceTimeline\(sourceId, title, uploadStart, uploadGeneration\)/, 'poller must receive upload ownership generation');
requirePattern(/var requestGeneration = \+\+S\.sourceTimelineRequestGeneration;/, 'selected-source requests must claim a generation');
requirePattern(/requestGeneration !== S\.sourceTimelineRequestGeneration \|\| S\.sourceTimelineSourceId !== sourceId/, 'stale persisted requests must not render');
requirePattern(/if \(!isRefresh\) \{\s*S\.sourceTimelineUploadGeneration\+\+;/, 'manual source selection must invalidate in-flight upload responses');
requirePattern(/var uploadGeneration = \+\+S\.sourceTimelineUploadGeneration;\s*S\.sourceTimelineRequestGeneration\+\+;\s*closeSourceTimelineProgress\(\);/, 'upload start must invalidate selected-source requests and close its progress stream');
requirePattern(/function ownsSourceTimelineStream\(\) \{\s*return S\.sourceTimelineEventSource === stream && S\.sourceTimelineSourceId === sourceId;\s*\}/, 'SSE ownership must be based on the active stream and source');
requirePattern(/var requestGeneration = \+\+S\.sourceTimelineRequestGeneration;\s*api\('GET', '\/sources\/' \+ sourceId\)\.then\(function\(r\) \{\s*if \(!ownsSourceTimelineStream\(\) \|\| requestGeneration !== S\.sourceTimelineRequestGeneration/, 'SSE-triggered source GETs must have their own stale-response guard');
requirePattern(/function scheduleSourceTimelinePoll\(sourceId\) \{[\s\S]*?S\.pollTimers\['tl-' \+ sourceId\] = setTimeout\(function\(\) \{ showTimelineForSource\(sourceId, true\); \}, 3000\);/, 'selected-source polling must have a shared fallback scheduler');
requirePattern(/function renderPersistedSourceTimeline\(sourceId, d\) \{[\s\S]*?scheduleSourceTimelinePoll\(sourceId\);/, 'persisted timeline rendering must retain fallback polling for non-terminal sources');
requirePattern(/if \(!ownsSourceTimelineStream\(\) \|\| requestGeneration !== S\.sourceTimelineRequestGeneration\) return;\s*if \(!r\.ok \|\| !r\.data\) \{ scheduleSourceTimelinePoll\(sourceId\); return; \}\s*renderPersistedSourceTimeline\(sourceId, r\.data\);/, 'SSE-triggered source GETs must use the poll-preserving persisted timeline renderer');

if (/var sourceRequestGeneration = S\.sourceTimelineRequestGeneration;/.test(page)) {
  throw new Error('SSE stream ownership must not capture the selected request generation at subscription time');
}

if (page.includes('if (S.sourceTimelineUploadSourceId && S.sourceTimelineUploadSourceId !== sourceId) return;')) {
  throw new Error('first upload must not retain ownership over newer uploads');
}

for (const script of page.matchAll(/<script>([\s\S]*?)<\/script>/g)) {
  new Function(script[1]);
}
