"""
V1 验收测试方案（test/v1/v1-acceptance-test-plan.md）P0-P3 阶段脚本共享工具函数。

不是独立脚本，被 test/v1/v1_p0_*.py ~ test/v1/v1_p3_*.py import。三类工具：
  1. HTTP：沿用 test/mvp/qa_accuracy_test.py 的 urllib 直连风格（POST /answer 等）；
  2. DB：只读打开 data/wiki-brain.db，核对 learning_events/traces/activation_links
     等表——方案第 3 节明确这些是 API 之外的"观察面"，很多状态（如 async trace_write
     产生的 activation_gap 事件）API 当前不提供按 trace_id 过滤的查询，只能读表；
  3. 题库解析：直接解析 v1-acceptance-test-plan.md 第 4 节的 markdown 表格，不手抄
     题面，避免和方案本身漂移（A/T/G 三组另与 test/mvp/mvp-acceptance-test-plan.md
     同源，但 B/C/D/E/F 组只存在于本方案，所以统一从本文件解析，不复用
     qa_accuracy_test.py 里针对 mvp 方案的 parse_question_bank）。
"""
import json
import re
import sqlite3
import time
import urllib.error
import urllib.request
import uuid
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent.parent
PLAN_PATH = REPO_ROOT / "test" / "v1" / "v1-acceptance-test-plan.md"
MARKDOWN_DIR = REPO_ROOT / "test" / "markdown"
RESULTS_DIR = REPO_ROOT / "test" / "v1" / "results"
DEFAULT_DB_PATH = REPO_ROOT / "data" / "wiki-brain.db"

SECTION_HEADINGS = {
    "A": r"### 4\.1 A 组",
    "T": r"### 4\.2 T 组",
    "B": r"### 4\.3 B 组",
    "C": r"### 4\.4 C 组",
    "D": r"### 4\.5 D 组",
    "E": r"### 4\.6 E 组",
    "F": r"### 4\.7 F 组",
    "G": r"### 4\.8 G 组",
}


# ---------------------------------------------------------------------------
# HTTP
# ---------------------------------------------------------------------------

def http_get_json(base_url, path, timeout=30):
    with urllib.request.urlopen(f"{base_url}{path}", timeout=timeout) as resp:
        return json.loads(resp.read().decode("utf-8"))


def http_get_text(base_url, path, timeout=30):
    with urllib.request.urlopen(f"{base_url}{path}", timeout=timeout) as resp:
        return resp.read().decode("utf-8")


def http_post_json(base_url, path, payload, timeout=180):
    data = json.dumps(payload).encode("utf-8")
    req = urllib.request.Request(
        f"{base_url}{path}",
        data=data,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        body = resp.read().decode("utf-8")
        return (json.loads(body) if body else None), resp.status


def http_delete_json(base_url, path, timeout=30):
    req = urllib.request.Request(f"{base_url}{path}", method="DELETE")
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        body = resp.read().decode("utf-8")
        return (json.loads(body) if body else None), resp.status


def http_post_multipart_file(base_url, path, file_path: Path, timeout=300):
    """上传文件到 POST /sources 或 /sources/:id/reupload（multipart form, field=file）。"""
    boundary = uuid.uuid4().hex
    filename = file_path.name
    data = file_path.read_bytes()

    body = bytearray()
    body += f"--{boundary}\r\n".encode("utf-8")
    body += (
        f'Content-Disposition: form-data; name="file"; filename="{filename}"\r\n'
    ).encode("utf-8")
    body += b"Content-Type: application/octet-stream\r\n\r\n"
    body += data
    body += f"\r\n--{boundary}--\r\n".encode("utf-8")

    req = urllib.request.Request(
        f"{base_url}{path}",
        data=bytes(body),
        method="POST",
        headers={"Content-Type": f"multipart/form-data; boundary={boundary}"},
    )
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return json.loads(resp.read().decode("utf-8")), resp.status
    except urllib.error.HTTPError as e:
        detail = e.read().decode("utf-8", "ignore")
        raise RuntimeError(f"HTTP {e.code}: {detail[:300]}") from e


def wait_for_server(base_url, timeout=10):
    try:
        http_get_json(base_url, "/sources?limit=1", timeout=timeout)
        return True
    except Exception:
        return False


def http_post_sse(base_url, path, payload, timeout=180):
    """消费 POST /answer/stream 的 SSE 响应，返回 event:result 携带的 AnswerResult
    JSON（与非流式 POST /answer 响应同构）。escapeSSE 只对 phase/thinking/content
    这几类多行文本事件做了 \\n -> \\ndata: 展开，event:result 的 payload 是
    json.Marshal 出的单行紧凑 JSON，不受影响，按"data: "行原样拼接即可。
    """
    data = json.dumps(payload).encode("utf-8")
    req = urllib.request.Request(
        f"{base_url}{path}",
        data=data,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    result = None
    error = None
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        event_type = None
        data_lines = []
        for raw_line in resp:
            line = raw_line.decode("utf-8", "replace").rstrip("\r\n")
            if line.startswith("event: "):
                event_type = line[len("event: "):]
                data_lines = []
            elif line.startswith("data: "):
                data_lines.append(line[len("data: "):])
            elif line == "":
                if event_type == "result" and data_lines:
                    try:
                        result = json.loads("\n".join(data_lines))
                    except json.JSONDecodeError:
                        pass
                elif event_type == "error":
                    error = "\n".join(data_lines)
                event_type, data_lines = None, []
    if result is None and error:
        raise RuntimeError(f"/answer/stream error event: {error}")
    return result


def ask_via_session(base_url, question, deep=False, timeout=180, session_id=None):
    """真实客户端路径（见 web/index.html）：POST /sessions -> POST /session/turn
    （解析 subject/intent/audience/constraint）-> POST /answer/stream（把这四个字段
    带进检索）。裸 POST /answer 不接受这四个字段、也不会做该解析——共现分组
    （question_kp_cooccurrence 按 subject 分组，见 trace/service.go 的
    normalize+groupKey 逻辑）和 ActivationLink 的对象/约束守门都依赖它们，直接调
    /answer 会导致同一问题的不同问法各自落入独立的字面 term 分组，永远无法累积
    confident_count。

    session_id=None 时每次新建一个空 session（模拟"新用户第一轮提问"，subject 由
    输入文本独立解析，跨问法可重复得到同一个 subject）；传入已有 session_id 可在
    同一会话内追问（用于 E 组会话追问测试）。

    返回 (turn, result)：turn 是 /session/turn 的原始响应；result 是与非流式
    POST /answer 同构的 AnswerResult dict（action != "retrieve" 时为 None，如
    触发了 clarify/interrupted，调用方需要自行处理）。
    """
    if session_id is None:
        sess, _ = http_post_json(base_url, "/sessions", {}, timeout=timeout)
        session_id = sess["session_id"]

    turn, _ = http_post_json(
        base_url, "/session/turn", {"session_id": session_id, "user_input": question}, timeout=timeout
    )
    if turn.get("action") != "retrieve":
        return turn, None

    eq = turn.get("expanded_query") or {}
    payload = {
        "question": eq.get("expanded_question") or question,
        "deep": deep,
        "session_id": session_id,
        "subject": eq.get("subject") or "",
        "intent": eq.get("intent") or "",
        "audience": eq.get("audience") or "",
        "constraint": eq.get("constraint") or "",
    }
    result = http_post_sse(base_url, "/answer/stream", payload, timeout=timeout)
    return turn, result


def poll_until(fn, timeout_s, interval_s=1.0):
    """轮询 fn() 直到返回真值（非 None/非 False）或超时，返回该值（超时则 None）。"""
    deadline = time.time() + timeout_s
    while True:
        val = fn()
        if val:
            return val
        if time.time() >= deadline:
            return None
        time.sleep(interval_s)


# ---------------------------------------------------------------------------
# DB（只读）——方案第 3 节"观察面"里 API 覆盖不到的部分，如按 trace_id 查 learning_events
# ---------------------------------------------------------------------------

def open_db(db_path=None, timeout=30):
    path = Path(db_path) if db_path else DEFAULT_DB_PATH
    if not path.exists():
        raise RuntimeError(f"数据库文件不存在: {path}")
    conn = sqlite3.connect(f"file:{path}?mode=ro", uri=True, timeout=timeout)
    conn.row_factory = sqlite3.Row
    return conn


def db_source_id_by_title(conn, title):
    row = conn.execute(
        "SELECT source_id FROM sources WHERE title = ? AND shadow_of IS NULL ORDER BY created_at DESC LIMIT 1",
        (title,),
    ).fetchone()
    return row["source_id"] if row else None


def db_trace_by_answer_id(conn, answer_id):
    row = conn.execute(
        "SELECT * FROM traces WHERE answer_id = ? ORDER BY created_at DESC LIMIT 1",
        (answer_id,),
    ).fetchone()
    return dict(row) if row else None


def db_learning_events_for_trace(conn, trace_id, event_type=None):
    if event_type:
        rows = conn.execute(
            "SELECT * FROM learning_events WHERE trace_id = ? AND event_type = ? ORDER BY created_at",
            (trace_id, event_type),
        ).fetchall()
    else:
        rows = conn.execute(
            "SELECT * FROM learning_events WHERE trace_id = ? ORDER BY created_at",
            (trace_id,),
        ).fetchall()
    return [dict(r) for r in rows]


def db_units_for_source(conn, source_id):
    rows = conn.execute(
        "SELECT * FROM knowledge_units WHERE source_id = ? ORDER BY line_start",
        (source_id,),
    ).fetchall()
    return [dict(r) for r in rows]


def db_points_for_source(conn, source_id):
    rows = conn.execute(
        "SELECT * FROM knowledge_points WHERE source_id = ?",
        (source_id,),
    ).fetchall()
    return [dict(r) for r in rows]


def db_shadow_sources(conn):
    rows = conn.execute("SELECT * FROM sources WHERE shadow_of IS NOT NULL").fetchall()
    return [dict(r) for r in rows]


def db_activation_link(conn, link_id):
    row = conn.execute(
        "SELECT * FROM activation_links WHERE link_id = ?", (link_id,)
    ).fetchone()
    return dict(row) if row else None


def db_learning_results_for_object(conn, object_id):
    rows = conn.execute(
        "SELECT * FROM learning_results WHERE object_id = ? ORDER BY created_at",
        (object_id,),
    ).fetchall()
    return [dict(r) for r in rows]


def db_learning_events_by_type(conn, event_type, since_created_at=None):
    """跨 trace 扫描某类型 learning_events（P11 用于观察 subject_synonym_gap 是否
    自然产生）。event_id 是 TEXT（非自增），增量轮询按 created_at 过滤。"""
    if since_created_at:
        rows = conn.execute(
            "SELECT * FROM learning_events WHERE event_type = ? AND created_at > ? ORDER BY created_at",
            (event_type, since_created_at),
        ).fetchall()
    else:
        rows = conn.execute(
            "SELECT * FROM learning_events WHERE event_type = ? ORDER BY created_at",
            (event_type,),
        ).fetchall()
    return [dict(r) for r in rows]


def db_links_for_source(conn, source_id, status=None):
    """找出 point 属于某 source 的 activation_links（P5/P6 判断"依赖旧 KP 的链接"用）。"""
    q = (
        "SELECT al.* FROM activation_links al "
        "JOIN knowledge_points kp ON al.point_id = kp.point_id "
        "WHERE kp.source_id = ?"
    )
    params = [source_id]
    if status:
        q += " AND al.status = ?"
        params.append(status)
    rows = conn.execute(q, params).fetchall()
    return [dict(r) for r in rows]


def db_kp_relations(conn, scope=None, relation_type=None):
    q = "SELECT * FROM knowledge_point_relations WHERE 1=1"
    params = []
    if scope:
        q += " AND scope = ?"
        params.append(scope)
    if relation_type:
        q += " AND relation_type = ?"
        params.append(relation_type)
    rows = conn.execute(q, params).fetchall()
    return [dict(r) for r in rows]


def db_wiki_pages(conn, status=None):
    q = "SELECT * FROM wiki_pages WHERE 1=1"
    params = []
    if status:
        q += " AND status = ?"
        params.append(status)
    rows = conn.execute(q, params).fetchall()
    return [dict(r) for r in rows]


def db_cooccurrence_for_point(conn, point_id):
    rows = conn.execute(
        "SELECT * FROM question_kp_cooccurrence WHERE point_id = ? ORDER BY confident_count DESC",
        (point_id,),
    ).fetchall()
    return [dict(r) for r in rows]


# ---------------------------------------------------------------------------
# 题库解析：test/v1-acceptance-test-plan.md 第 4 节
# ---------------------------------------------------------------------------

_TABLE_ROW_RE = re.compile(r"^\|(.+)\|\s*$")
_TABLE_SEP_RE = re.compile(r"^\|[\s:|-]+\|\s*$")


def parse_md_tables(text):
    """解析文本片段里全部 pipe table，每张表返回 list[dict]（列名取自表头行）。"""
    tables = []
    lines = text.splitlines()
    i = 0
    while i < len(lines):
        line = lines[i].strip()
        if (
            _TABLE_ROW_RE.match(line)
            and i + 1 < len(lines)
            and _TABLE_SEP_RE.match(lines[i + 1].strip())
        ):
            headers = [c.strip() for c in line.strip("|").split("|")]
            i += 2
            rows = []
            while i < len(lines) and _TABLE_ROW_RE.match(lines[i].strip()):
                cells = [c.strip() for c in lines[i].strip().strip("|").split("|")]
                if len(cells) == len(headers):
                    rows.append(dict(zip(headers, cells)))
                i += 1
            tables.append(rows)
        else:
            i += 1
    return tables


def load_plan_text():
    return PLAN_PATH.read_text(encoding="utf-8")


def section_text(full_text, heading_prefix_regex):
    pattern = heading_prefix_regex + r"[^\n]*\n(.*?)(?=\n#{1,3} |\Z)"
    m = re.search(pattern, full_text, re.S)
    if not m:
        raise RuntimeError(f"在方案里找不到章节: {heading_prefix_regex}")
    return m.group(1)


def load_group(group_letter, full_text=None):
    """返回某组（A/T/B/C/D/E/F/G）全部行（dict），按表头原始列名。"""
    full_text = full_text or load_plan_text()
    heading = SECTION_HEADINGS[group_letter]
    tables = parse_md_tables(section_text(full_text, heading))
    rows = []
    for t in tables:
        rows.extend(t)
    return rows


def question_cell_key(row):
    for k in row:
        if "问题" in k:
            return k
    return None


def question_variants(row):
    """把「主问法 / 变体1 / 变体2」列拆成 list[str]；单问法的组（B/C/D/G）返回单元素 list。"""
    key = question_cell_key(row)
    if not key:
        return []
    cell = row[key]
    return [q.strip() for q in re.split(r"\s*/\s*", cell) if q.strip()]


def row_id(row):
    for k in ("ID",):
        if k in row:
            return row[k]
    return None


# ---------------------------------------------------------------------------
# 领域分组（方案第 6 节：制度域 / 技术域）
# ---------------------------------------------------------------------------

def domain_of(qid):
    if qid.startswith("A"):
        return "制度域"
    if qid.startswith("T"):
        return "技术域"
    if qid.startswith("G"):
        n = int(re.sub(r"\D", "", qid))
        return "制度域" if n <= 24 else "技术域"
    if qid.startswith("B"):
        n = int(re.sub(r"\D", "", qid))
        return "制度域" if n <= 2 else "技术域"
    if qid.startswith("D"):
        # 方案第 6 节正确率表只枚举了 D1-D4(制度)/D5-D7(技术)，未提 D8——但 D8
        # （万相公文提货价）内容上明显是制度域，按内容而非该表的枚举遗漏归类。
        n = int(re.sub(r"\D", "", qid))
        return "技术域" if n in (5, 6, 7) else "制度域"
    if qid.startswith("C"):
        return "缺口"
    if qid.startswith("F"):
        return "技术域"
    return "未知"


# ---------------------------------------------------------------------------
# 关键词覆盖率核对（沿用 qa_accuracy_test.py 的口径）
# ---------------------------------------------------------------------------

UNIT_NUMBER_RE = re.compile(
    r"\d+(?:\.\d+)?\s*(?:%|天|元|分|次|个月|小时|台|条|倍|港币|年|周|万|亿|人|级|档)"
)
BARE_NUMBER_RE = re.compile(r"(?<!\d)\d{2,}(?:\.\d+)?(?!\d)|(?<!\d)\d+\.\d+(?!\d)")


def extract_key_terms(points_text):
    """从「期望答案要点」里抽取可机器核对的关键词：反引号代码片段 + 数字(+单位)。"""
    terms = set(re.findall(r"`([^`]+)`", points_text))
    consumed = set()
    for m in UNIT_NUMBER_RE.finditer(points_text):
        terms.add(re.sub(r"\s+", "", m.group(0)))
        consumed.add(m.span())
    for m in BARE_NUMBER_RE.finditer(points_text):
        if any(m.start() >= s and m.end() <= e for s, e in consumed):
            continue
        terms.add(m.group(0))
    return sorted(terms)


# ---------------------------------------------------------------------------
# Source/Evidence 辅助
# ---------------------------------------------------------------------------

def fetch_source_titles(base_url):
    """GET /sources 分页拉全量，建 source_id -> title 映射。"""
    id_to_title = {}
    offset = 0
    limit = 50
    while True:
        data = http_get_json(base_url, f"/sources?limit={limit}&offset={offset}")
        items = data.get("items") or data.get("sources") or []
        if not items:
            break
        for it in items:
            id_to_title[it["source_id"]] = it["title"]
        if len(items) < limit:
            break
        offset += limit
    return id_to_title


def evidence_source_ids(evidence_list):
    ids = []
    for ev in evidence_list or []:
        ref = ev.get("source_ref")
        if not ref:
            continue
        if isinstance(ref, str):
            try:
                ref = json.loads(ref)
            except json.JSONDecodeError:
                continue
        sid = ref.get("source_id")
        if sid:
            ids.append(sid)
    return ids


def list_markdown_files():
    return sorted(MARKDOWN_DIR.glob("*.md"))


# 方案第 4 节「期望证据/来源」列里的缩写 -> test/markdown/*.md 文件名（去扩展名，即
# 上传后 Source.title 的实际值，见 internal/source/service.go Import 的 Title 赋值）。
# 一个缩写可能对应多个候选标题（如"两篇 RAC 部署文档"），命中其一即算命中。
SOURCE_ABBREV_TO_TITLES = {
    # 每个缩写下同时列出 test/markdown/*.md 的标准文件名（全新空库场景）和
    # 已知的 MVP 遗留库里的实际 Source.title（同一份文档曾用 docx/pdf 原始文件名
    # 上传，标题写法略有出入，如"K8S部署" vs "k8s部署"）——命中其一即算 direct_hit，
    # 两套环境都能跑，不必强制清库。
    "报销规定": ["日常费用报销期限管理规定"],
    "差旅费": ["差旅费报销制度"],
    "培训积分": ["培训积分管理办法"],
    "平台办法": ["大模型开发测试基础平台使用暂行管理办法"],
    "应收账款": ["应收账款管理制度"],
    "项目考核": ["项目考核与激励制度"],
    "万相公文": ["万相公文销售奖励制度"],
    "绩效管理": ["绩效管理制度"],
    "无合同立项": ["无合同立项申请与审批规范"],
    "考勤管理": ["考勤管理管理制度", "考勤管理制度"],
    "docker swarm": ["Docker Swam 集群部署", "Dock Swam 集群部署"],
    "swarm": ["Docker Swam 集群部署", "Dock Swam 集群部署"],
    "k8s": ["K8S部署", "k8s部署"],
    "mysql": ["MYSQL 主从热备", "MYSQL 主从热备部署"],
    "rac 开启归档": ["Oracle RAC 开启归档"],
    "rac 问题汇总": ["Oracle RAC 问题汇总"],
    "达梦": ["达梦数据库优化"],
    "神通": ["神通数据库优化"],
    "金仓": ["金仓数据库优化"],
    "alwayson": ["SQL Server AlwaysOn 安装配置"],
    "19c rac": ["Oracle 19c RAC 集群安装部署维护环境", "Oracle 19c RAC 集群安装部署维护"],
    "11g rac": ["Oracle 11g RAC 集群安装部署维护环境", "Oracle 11g RAC 集群安装部署维护"],
    "两篇 rac": [
        "Oracle 11g RAC 集群安装部署维护环境",
        "Oracle 19c RAC 集群安装部署维护环境",
        "Oracle 11g RAC 集群安装部署维护",
        "Oracle 19c RAC 集群安装部署维护",
    ],
    "两篇优化文档": ["达梦数据库优化", "金仓数据库优化", "神通数据库优化"],
    "三篇优化文档": ["达梦数据库优化", "金仓数据库优化", "神通数据库优化"],
}


def expected_titles_for(source_text):
    """把「报销规定·第一条」这类"期望证据"文本解析成候选 Source.title 列表。

    一条文本可能用 "+" 连接多个来源（如 B 组"应收账款·第十二条 + 项目考核·5.3"），
    每个片段独立解析，结果去重合并。
    """
    titles = []
    for part in re.split(r"\s*\+\s*", source_text):
        abbrev = part.split("·", 1)[0].strip().lower()
        if not abbrev:
            continue
        matched = SOURCE_ABBREV_TO_TITLES.get(abbrev)
        if matched is None:
            for key, vals in SOURCE_ABBREV_TO_TITLES.items():
                if key.lower() in abbrev or abbrev in key.lower():
                    matched = vals
                    break
        if matched:
            for t in matched:
                if t not in titles:
                    titles.append(t)
    return titles


# ---------------------------------------------------------------------------
# 报告输出
# ---------------------------------------------------------------------------

def now_stamp():
    return time.strftime("%Y%m%d-%H%M%S")


def write_jsonl(records, out_dir: Path, name_prefix: str):
    out_dir.mkdir(parents=True, exist_ok=True)
    path = out_dir / f"{name_prefix}_{now_stamp()}.jsonl"
    with path.open("w", encoding="utf-8") as f:
        for r in records:
            f.write(json.dumps(r, ensure_ascii=False, default=str) + "\n")
    return path


def write_text(text, out_dir: Path, name_prefix: str):
    out_dir.mkdir(parents=True, exist_ok=True)
    path = out_dir / f"{name_prefix}_{now_stamp()}.md"
    path.write_text(text, encoding="utf-8")
    return path
