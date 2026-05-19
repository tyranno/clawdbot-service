// worktime-monitor.js
// Monitors worktime.txt, updates Notion, returns status for cron
const fs = require('fs');
const path = require('path');

const NOTION_KEY = fs.readFileSync(path.join(require('os').homedir(), '.config/notion/api_key'), 'utf-8').trim();
const DB_ID = '2fc10451-8bef-81a7-b727-000b6c6ed2aa'; // data_source_id
const DATABASE_ID = '2fc10451-8bef-8127-a315-fee134b37ae6'; // database_id (for creating pages)
const STATE_FILE = path.join(__dirname, 'worktime-monitor-state.json');
const WORKTIME_FILE = 'C:\\Users\\lab\\업무시간기록\\worktime2.txt';

function loadState() {
  try { return JSON.parse(fs.readFileSync(STATE_FILE, 'utf-8')); }
  catch { return { lastLine: 0, lastClockIn: null, lastClockOut: null, todayPageId: null, today: null }; }
}

function saveState(state) {
  fs.writeFileSync(STATE_FILE, JSON.stringify(state, null, 2));
}

async function notionRequest(endpoint, method, body) {
  const res = await fetch(`https://api.notion.com/v1/${endpoint}`, {
    method,
    headers: {
      'Authorization': `Bearer ${NOTION_KEY}`,
      'Notion-Version': '2025-09-03',
      'Content-Type': 'application/json'
    },
    body: body ? JSON.stringify(body) : undefined
  });
  return res.json();
}

async function findTodayPage(dateStr) {
  const data = await notionRequest(`data_sources/${DB_ID}/query`, 'POST', {
    filter: { property: 'Date', title: { equals: dateStr } }
  });
  return data.results?.[0] || null;
}

async function createPage(dateStr, clockIn, dayOfWeek) {
  return notionRequest('pages', 'POST', {
    parent: { database_id: DATABASE_ID },
    properties: {
      Date: { title: [{ text: { content: dateStr } }] },
      ClockIn: { rich_text: [{ text: { content: clockIn } }] },
      DayOfWeek: { rich_text: [{ text: { content: dayOfWeek } }] },
      WorkDate: { date: { start: dateStr } },
      Status: { select: { name: 'Working' } }
    }
  });
}

async function updateClockOut(pageId, clockOut) {
  return notionRequest(`pages/${pageId}`, 'PATCH', {
    properties: {
      ClockOut: { rich_text: [{ text: { content: clockOut } }] },
      Status: { select: { name: 'Done' } }
    }
  });
}

const DOW = ['일', '월', '화', '수', '목', '금', '토'];

async function main() {
  const state = loadState();
  const content = fs.readFileSync(WORKTIME_FILE, 'utf-8');
  const lines = content.split('\n').filter(l => l.trim());
  
  const today = new Date().toLocaleDateString('en-CA', { timeZone: 'Asia/Seoul' }); // YYYY-MM-DD
  const events = [];
  
  // Parse new lines since last check
  const startIdx = state.lastLine || 0;
  const newLines = lines.slice(startIdx);
  
  for (const line of newLines) {
    const clockInMatch = line.match(/^출근 - (\d{4}-\d{2}-\d{2}) (\d{2}:\d{2}:\d{2})/);
    const clockOutMatch = line.match(/^퇴근 - (\d{4}-\d{2}-\d{2}) (\d{2}:\d{2}:\d{2})/);
    const summaryMatch = line.match(/^=== (\d{4}-\d{2}-\d{2}) 근무시간: (.+)===/);
    
    if (clockInMatch) {
      events.push({ type: 'clockIn', date: clockInMatch[1], time: clockInMatch[2] });
    } else if (clockOutMatch) {
      events.push({ type: 'clockOut', date: clockOutMatch[1], time: clockOutMatch[2] });
    } else if (summaryMatch) {
      // 오늘 날짜 summary만 처리 (과거 날짜 재전송 방지)
      if (summaryMatch[1] === today) {
        events.push({ type: 'summary', date: summaryMatch[1], hours: summaryMatch[2].trim() });
      }
    }
  }
  
  state.lastLine = lines.length;
  
  const results = [];
  
  for (const evt of events) {
    // 오늘 날짜 이벤트만 처리 (과거 날짜 재전송 방지)
    if (evt.date !== today) continue;

    if (evt.type === 'clockIn') {
      // Check if already processed today
      if (state.today === evt.date && state.lastClockIn) continue;
      
      const dow = DOW[new Date(evt.date + 'T00:00:00+09:00').getDay()];
      
      // Check if page exists
      let page = await findTodayPage(evt.date);
      if (!page) {
        page = await createPage(evt.date, evt.time, dow);
        results.push(`🟢 출근 기록: ${evt.date} (${dow}) ${evt.time}`);
      }
      
      state.today = evt.date;
      state.todayPageId = page.id;
      state.lastClockIn = evt.time;
    } else if (evt.type === 'clockOut') {
      // Find or use cached page
      let pageId = state.todayPageId;
      if (!pageId || state.today !== evt.date) {
        const page = await findTodayPage(evt.date);
        if (page) pageId = page.id;
      }
      
      if (pageId && state.lastClockOut !== evt.time) {
        await updateClockOut(pageId, evt.time);
        results.push(`🔴 퇴근 기록: ${evt.date} ${evt.time}`);
        state.lastClockOut = evt.time;
        state.todayPageId = pageId;
        state.today = evt.date;
      }
    } else if (evt.type === 'summary') {
      results.push(`📊 ${evt.date} 근무시간: ${evt.hours}`);
    }
  }
  
  saveState(state);
  
  if (results.length > 0) {
    console.log(results.join('\n'));
  } else {
    console.log('NO_CHANGE');
  }
}

main().catch(e => { console.error(e); process.exit(1); });
