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
requirePattern(/api\('GET', '\/sources\/' \+ sourceId\)\.then\(function\(r\) \{\s*if \(sourceRequestGeneration !== S\.sourceTimelineRequestGeneration \|\| S\.sourceTimelineSourceId !== sourceId/, 'SSE-triggered source GETs must be guarded by the selected request generation');

if (page.includes('if (S.sourceTimelineUploadSourceId && S.sourceTimelineUploadSourceId !== sourceId) return;')) {
  throw new Error('first upload must not retain ownership over newer uploads');
}

for (const script of page.matchAll(/<script>([\s\S]*?)<\/script>/g)) {
  new Function(script[1]);
}
