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
requirePattern(/id="sourceNameFilter"/, 'file drawer must expose a file-name search input');
requirePattern(/var nameFilter = document\.getElementById\('sourceNameFilter'\)\?\.value \|\| '';/, 'loadSources must read the file-name filter');
requirePattern(/if \(nameFilter\) params \+= '&q=' \+ encodeURIComponent\(nameFilter\);/, 'loadSources must forward file-name search to the sources API');
requirePattern(/\.drawer-body \{ flex:1; overflow:hidden; display:flex; \}/, 'shared drawer body must stay a horizontal flex row for split drawers');
requirePattern(/#fileDrawer \.drawer-body \{ flex-direction:column; gap:12px; padding:16px; \}/, 'file drawer body must stack top controls above the source list');
requirePattern(/\.drawer-top \{ display:flex; gap:12px; flex-shrink:0;/, 'file drawer must expose a top row for upload and timeline');
requirePattern(/\.drawer-upload \{ width:200px;[^}]*overflow:hidden;/, 'upload panel must be compact and hide overflow instead of scrolling');
requirePattern(/\.drawer-timeline \{ flex:1;[^}]*overflow:hidden;/, 'timeline panel must sit beside the upload panel without a scrollbar');
requirePattern(/\.timeline-steps \{ display:flex;/, 'upload process steps must render horizontally');
requirePattern(/html \+= '<\/div><div class="timeline-steps">';/, 'renderTimeline must wrap steps in a horizontal container');
requirePattern(/tl-line tl-line-left/, 'timeline dots must sit between left/right connectors so they center over labels');
requirePattern(/\.source-card \{[^}]*display:flex;[^}]*align-items:center;/, 'source cards must vertically center action buttons beside the file info');
requirePattern(/\.source-card \.sc-actions \{[^}]*justify-content:flex-end;/, 'source card actions must right-align');
requirePattern(/class="sc-main"/, 'source card content must wrap title and meta separately from actions');
requirePattern(/<div class="drawer-top">[\s\S]*?<div class="drawer-upload">[\s\S]*?<div class="drawer-timeline">[\s\S]*?id="timelinePanel"/, 'upload and timeline areas must render side-by-side above the source list');
requirePattern(/<div style="padding:8px 12px;border-bottom:1px solid var\(--border-lt\);display:flex;gap:8px;flex-wrap:nowrap;align-items:center;">[\s\S]*?id="sourceStatusFilter"[\s\S]*?id="sourceDomainFilter"[\s\S]*?id="sourceNameFilter"/, 'status filter, domain filter, and filename search must stay inline in one row');
requirePattern(/#sourceNameFilter \{[\s\S]*?border:1px solid var\(--border\);[\s\S]*?border-radius:var\(--radius-xs\);[\s\S]*?font-size:13px;[\s\S]*?background:var\(--bg\);[\s\S]*?color:var\(--text\);/, 'filename search input must match the select styling');

if (/var sourceRequestGeneration = S\.sourceTimelineRequestGeneration;/.test(page)) {
  throw new Error('SSE stream ownership must not capture the selected request generation at subscription time');
}

if (page.includes('if (S.sourceTimelineUploadSourceId && S.sourceTimelineUploadSourceId !== sourceId) return;')) {
  throw new Error('first upload must not retain ownership over newer uploads');
}

for (const script of page.matchAll(/<script>([\s\S]*?)<\/script>/g)) {
  new Function(script[1]);
}
