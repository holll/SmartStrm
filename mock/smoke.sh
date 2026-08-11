#!/bin/bash
# SmartStrm-Go 冒烟测试（无配置文件模式）：mock OpenList + 真实服务 + 生成验证
set -e
cd "$(dirname "$0")/.."

SMOKE_DIR="C:/Users/Administrator/AppData/Local/Temp/ssmoke"
STRM_DIR="$SMOKE_DIR/strm"
DB="$SMOKE_DIR/data/data.db"
rm -rf "$SMOKE_DIR"
mkdir -p "$SMOKE_DIR" "$STRM_DIR"

# 1. 启动 mock openlist
./build/mock-openlist.exe &
MOCK_PID=$!
sleep 1

# 2. 启动 smartstrm（-port 参数，数据库固定 data/data.db，首次随机密码）
cd "$SMOKE_DIR"
"C:/Users/Administrator/GolandProjects/smartstrm-go/build/smartstrm.exe" -port 18024 > "$SMOKE_DIR/server.log" 2>&1 &
SS_PID=$!
sleep 2
ADMIN_PWD=$(python -c "import re;print(re.search(r'admin / ([0-9a-f]+)', open(r'$SMOKE_DIR/server.log', encoding='utf-8').read()).group(1))")
echo "初始密码: $ADMIN_PWD"
cd "C:/Users/Administrator/GolandProjects/smartstrm-go"

echo "== 1. 登录 =="
TOKEN=$(curl -s -X POST http://127.0.0.1:18024/api/login -H "Content-Type: application/json" -d "{\"username\":\"admin\",\"password\":\"$ADMIN_PWD\"}" | python -c "import sys,json;print(json.load(sys.stdin)['token'])")
echo "token: ${TOKEN:0:8}..."

AUTH="Authorization: Bearer $TOKEN"

echo "== 1.5 通过 Web API 配置存储/任务/STRM 设置 =="
curl -s -X POST http://127.0.0.1:18024/api/storages -H "$AUTH" -H "Content-Type: application/json" \
  -d '{"name":"mock","driver":"openlist","url":"http://127.0.0.1:19090","token":"mock-token"}' > /dev/null
curl -s -X PUT http://127.0.0.1:18024/api/settings -H "$AUTH" -H "Content-Type: application/json" \
  -d "{\"strm\":{\"media_ext\":[\"mp4\",\"mkv\",\"mov\",\"avi\",\"wmv\"],\"media_size\":20,\"copy_ext\":[\"nfo\",\"jpg\",\"png\",\"ass\",\"srt\"],\"save_dir\":\"$STRM_DIR\",\"url_encode\":true,\"gen_type\":\"path\",\"strm_base\":\"http://127.0.0.1:19090\"}}" > /dev/null
curl -s -X PUT http://127.0.0.1:18024/api/plugins/custom_strm_name -H "$AUTH" -H "Content-Type: application/json" \
  -d '{"enabled":true,"custom_name":"{name}.strm"}' > /dev/null
curl -s -X POST http://127.0.0.1:18024/api/tasks -H "$AUTH" -H "Content-Type: application/json" \
  -d '{"name":"FC2","storage":"mock","storage_path":"/115/FC2","incremental":true,"dir_time_check":true,"plugins":{"skip_keyword":{"enabled":true,"keywords":"advert"}}}' > /dev/null
# 配置 Emby 删除同步并获取 webhook token
WH_TOKEN=$(curl -s http://127.0.0.1:18024/api/webhook/info -H "$AUTH" | python -c "import sys,json;print(json.load(sys.stdin)['token'])")
curl -s -X PUT http://127.0.0.1:18024/api/webhook -H "$AUTH" -H "Content-Type: application/json" \
  -d '{"emby_delete_sync":{"enabled":true,"strm_in_emby":"/strm","storage_path_map":"/115","storage":"mock","allowed_prefix":["/115/FC2","/115/AV"]}}' > /dev/null
echo "配置完成，webhook token: $WH_TOKEN"

echo "== 2. 运行任务 FC2 =="
curl -s -X POST http://127.0.0.1:18024/api/tasks/FC2/run -H "$AUTH"
echo

echo "== 3. 等待任务完成 =="
sleep 6
curl -s http://127.0.0.1:18024/api/tasks/status -H "$AUTH" | python -c "import sys,json;s=json.load(sys.stdin)['FC2'];print('running:',s['running'],'result:',s['result'])"

echo "== 3.5 再次运行验证目录时间检查（应跳过未变化目录） =="
curl -s -X POST http://127.0.0.1:18024/api/tasks/FC2/run -H "$AUTH" > /dev/null
sleep 4
curl -s http://127.0.0.1:18024/api/tasks/status -H "$AUTH" | python -c "import sys,json;s=json.load(sys.stdin)['FC2'];print('二次运行 skipped:',s['result']['skipped'])"

echo "== 4. 验证生成的 STRM 文件 =="
cat "$STRM_DIR/FC2/A/ADN-468.strm"
echo
echo "--- 刮削文件（应已复制）---"
cat "$STRM_DIR/FC2/A/ADN-468.nfo" 2>/dev/null || echo "(缺失)"
echo "--- 检查：advert 应被关键词插件过滤、tiny.mp4 应被大小阈值过滤 ---"

echo "== 5. Emby 删除同步测试 =="
curl -s -X POST "http://127.0.0.1:18024/webhook/emby/$WH_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"Event":"item.deleted","Item":{"Path":"/strm/FC2/A/ADN-468.strm"}}'
echo

echo "== 6. Webhook 触发任务测试 =="
curl -s -X POST "http://127.0.0.1:18024/webhook/$WH_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"strmtask":"FC2"}'
echo

echo "== 7. 运行记录落库 =="
curl -s "http://127.0.0.1:18024/api/runs?limit=3" -H "$AUTH" | python -c "import sys,json;[print(' ',r['id'],r['status'],'gen='+str(r['generated'])) for r in json.load(sys.stdin)]"

echo "== 完成，清理进程 =="
kill $SS_PID $MOCK_PID 2>/dev/null || true
