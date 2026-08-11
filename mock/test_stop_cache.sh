#!/bin/bash
# 回归测试：目录时间检查缓存时序 bug
# 场景：任务运行中途停止 → 未完整处理的目录不得记录 mtime 缓存
#       下次运行必须重新扫描这些目录（否则 strm 永远不会生成）
set -e
cd "$(dirname "$0")/.."

SMOKE_DIR="C:/Users/Administrator/AppData/Local/Temp/ssmoke"
STRM_DIR="$SMOKE_DIR/strm"
rm -rf "$SMOKE_DIR"
mkdir -p "$STRM_DIR"

./build/mock-openlist.exe > /dev/null 2>&1 &
MOCK_PID=$!
sleep 1


cd "$SMOKE_DIR"
"C:/Users/Administrator/GolandProjects/smartstrm-go/build/smartstrm.exe" -port 18024 > "$SMOKE_DIR/server.log" 2>&1 &
SS_PID=$!
sleep 2
ADMIN_PWD=$(python -c "import re;print(re.search(r'admin / ([0-9a-f]+)', open(r'$SMOKE_DIR/server.log', encoding='utf-8').read()).group(1))")
cd "C:/Users/Administrator/GolandProjects/smartstrm-go"

TOKEN=$(curl -s -X POST http://127.0.0.1:18024/api/login -H "Content-Type: application/json" -d "{\"username\":\"admin\",\"password\":\"$ADMIN_PWD\"}" | python -c "import sys,json;print(json.load(sys.stdin)['token'])")
AUTH="Authorization: Bearer $TOKEN"

# 通过 API 配置存储/任务/STRM 设置
curl -s -X POST http://127.0.0.1:18024/api/storages -H "$AUTH" -H "Content-Type: application/json" -d '{"name":"mock","driver":"openlist","url":"http://127.0.0.1:19090","token":"mock-token"}' > /dev/null
curl -s -X PUT http://127.0.0.1:18024/api/settings -H "$AUTH" -H "Content-Type: application/json" -d "{\"strm\":{\"media_ext\":[\"mp4\"],\"media_size\":0,\"copy_ext\":[\"nfo\"],\"save_dir\":\"$STRM_DIR\",\"url_encode\":true,\"gen_type\":\"path\",\"strm_base\":\"\"}}" > /dev/null
curl -s -X POST http://127.0.0.1:18024/api/tasks -H "$AUTH" -H "Content-Type: application/json" -d '{"name":"FC2","storage":"mock","storage_path":"/115/FC2","incremental":true,"dir_time_check":true}' > /dev/null

echo "== 1. 启动任务，1.5 秒后停止（50 个子目录，列表延时 80ms） =="
curl -s -X POST http://127.0.0.1:18024/api/tasks/FC2/run -H "$AUTH" > /dev/null
sleep 1.5
curl -s -X POST http://127.0.0.1:18024/api/tasks/FC2/stop -H "$AUTH"
sleep 2

echo "== 2. 检查停止后 dir_cache 状态 =="
CACHE="$STRM_DIR/FC2/.smartstrm/dir_cache.json"
if [ ! -f "$CACHE" ]; then echo "FAIL: dir_cache.json 不存在"; exit 1; fi
python - <<'EOF'
import json
cache = json.load(open("C:/Users/Administrator/AppData/Local/Temp/ssmoke/strm/FC2/.smartstrm/dir_cache.json"))
# 中断应发生在 D01-D50 扫描途中：已完整处理的目录有记录，未处理的没有
print("缓存目录数:", len(cache))
assert len(cache) < 50, f"FAIL: 停止后不应记录全部 50 个目录（实际 {len(cache)}）"
EOF
echo "PASS: 未完整处理的目录未被记录（缓存数 < 50）"

echo "== 3. 二次运行：未记录的目录必须被重新扫描 =="
curl -s -X POST http://127.0.0.1:18024/api/tasks/FC2/run -H "$AUTH" > /dev/null
sleep 8
curl -s http://127.0.0.1:18024/api/tasks/status -H "$AUTH" | python -c "
import sys,json
d = json.load(sys.stdin)
s = d['FC2']
g = s['result']['generated'] if s['result'] else 0
print('二次运行 generated:', g)
assert g > 10, f'FAIL: 二次运行只生成 {g}，未完成目录被跳过（bug 复现）'
assert g < 50, f'FAIL: 异常，生成数 {g} 不预期'
print('PASS: 二次运行重新扫描了未完成目录（', g, '个）')
"

echo "== 全部通过 =="
kill $SS_PID $MOCK_PID 2>/dev/null || true
