/**
 * 주식 조건 검색 스크리너
 * - 네이버 증권 API 기반
 * - 크롤링 툴 불필요 (JSON API 직접 호출)
 * 
 * 조건:
 *  1. 시가총액 1조 이상
 *  2. PBR 낮은 종목 (기본: 1.0 이하)
 *  3. 최근 3일 거래량 증가 추세
 *  4. 전일 대비 거래량 100% 이상
 * 
 * 사용법: node screener.js [--pbr 1.0] [--market-cap 10000] [--vol-ratio 100] [--json] [--top 20]
 */

const https = require('https');
const http = require('http');

// ============ 설정 ============
const DEFAULT_CONFIG = {
  maxPBR: 1.0,              // PBR 상한 (배)
  minMarketCap: 10000,      // 최소 시가총액 (억원) → 1조 = 10000억
  minVolRatio: 100,         // 전일 대비 거래량 비율 (%)
  minTurnover: 0.3,         // 최소 회전율 (거래대금/시총, %)
  minTradingValue: 30,      // 최소 거래대금 (억원)
  aboveMA: 200,             // N일 이동평균선 위 종가 (0=비활성)
  maRisingOnly: true,        // 이평선 상승 중인 종목만
  volumeDays: 3,            // 거래량 증가 확인 일수
  top: 30,                  // 최대 출력 수
  markets: ['KOSPI', 'KOSDAQ'],
  pageSize: 100,
  outputJson: false,
  requestDelay: 80,         // API 호출 간 딜레이 (ms)
};

// ============ CLI 파싱 ============
function parseArgs() {
  const args = process.argv.slice(2);
  const config = { ...DEFAULT_CONFIG };
  
  for (let i = 0; i < args.length; i++) {
    switch (args[i]) {
      case '--pbr': config.maxPBR = parseFloat(args[++i]); break;
      case '--market-cap': config.minMarketCap = parseInt(args[++i]); break;
      case '--vol-ratio': config.minVolRatio = parseFloat(args[++i]); break;
      case '--days': config.volumeDays = parseInt(args[++i]); break;
      case '--top': config.top = parseInt(args[++i]); break;
      case '--json': config.outputJson = true; break;
      case '--market': config.markets = [args[++i].toUpperCase()]; break;
      case '--turnover': config.minTurnover = parseFloat(args[++i]); break;
      case '--trading-value': config.minTradingValue = parseFloat(args[++i]); break;
      case '--ma': config.aboveMA = parseInt(args[++i]); break;
      case '--no-ma-rising': config.maRisingOnly = false; break;
      case '--delay': config.requestDelay = parseInt(args[++i]); break;
      case '--help':
        console.log(`
주식 조건 검색 스크리너
=======================
사용법: node screener.js [옵션]

옵션:
  --pbr <값>         PBR 상한 (기본: 1.0)
  --market-cap <억>  최소 시가총액 (억원, 기본: 10000 = 1조)
  --vol-ratio <%%>    전일대비 거래량 비율 (기본: 100%%)
  --days <일>        거래량 증가 확인 일수 (기본: 3)
  --top <개>         최대 출력 수 (기본: 30)
  --market <시장>    KOSPI 또는 KOSDAQ (기본: 둘 다)
  --delay <ms>       API 호출 간 딜레이 (기본: 80ms)
  --json             JSON 형태로 출력
  --help             도움말
        `);
        process.exit(0);
    }
  }
  return config;
}

// ============ HTTP 요청 ============
function fetchJSON(url) {
  return new Promise((resolve, reject) => {
    const mod = url.startsWith('https') ? https : http;
    const req = mod.get(url, {
      headers: {
        'User-Agent': 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36',
        'Accept': 'application/json',
      },
      timeout: 10000,
    }, (res) => {
      let data = '';
      res.on('data', chunk => data += chunk);
      res.on('end', () => {
        try {
          resolve(JSON.parse(data));
        } catch (e) {
          reject(new Error(`JSON parse error: ${e.message} (url: ${url})`));
        }
      });
    });
    req.on('error', reject);
    req.on('timeout', () => { req.destroy(); reject(new Error('timeout')); });
  });
}

function sleep(ms) {
  return new Promise(r => setTimeout(r, ms));
}

// ============ 네이버 API ============

// 시가총액순 전종목 리스트 (페이지네이션)
async function fetchStockList(market, pageSize = 100) {
  const allStocks = [];
  let page = 1;
  
  while (true) {
    const url = `https://m.stock.naver.com/api/stocks/marketValue/${market}?page=${page}&pageSize=${pageSize}`;
    const data = await fetchJSON(url);
    
    if (!data.stocks || data.stocks.length === 0) break;
    
    for (const s of data.stocks) {
      const marketValue = parseInt((s.marketValue || '0').replace(/,/g, ''));
      allStocks.push({
        code: s.itemCode,
        name: s.stockName,
        market,
        marketValue,       // 억원
        closePrice: s.closePrice,
        fluctuation: s.fluctuationsRatio,
      });
    }
    
    // 시총이 기준 이하면 더 볼 필요 없음
    const lastStock = allStocks[allStocks.length - 1];
    if (lastStock.marketValue < DEFAULT_CONFIG.minMarketCap) break;
    
    page++;
    await sleep(50);
  }
  
  return allStocks;
}

// 종목 상세 (PBR, 거래량 등)
async function fetchStockDetail(code) {
  const url = `https://m.stock.naver.com/api/stock/${code}/integration`;
  const data = await fetchJSON(url);
  
  const result = {};
  if (data.totalInfos) {
    for (const info of data.totalInfos) {
      switch (info.code) {
        case 'pbr':
          result.pbr = parseFloat((info.value || '0').replace(/[^0-9.]/g, ''));
          break;
        case 'per':
          result.per = parseFloat((info.value || '0').replace(/[^0-9.]/g, ''));
          break;
        case 'accumulatedTradingVolume':
          result.todayVolume = parseInt((info.value || '0').replace(/,/g, ''));
          break;
        case 'marketValue':
          result.marketValueText = info.value;
          break;
        case 'dividendYieldRatio':
          result.dividendYield = info.value;
          break;
      }
    }
  }
  return result;
}

// 일별 차트 (거래량 추이)
async function fetchDailyChart(code, days = 5) {
  const end = new Date();
  const start = new Date();
  start.setDate(start.getDate() - Math.ceil(days * 1.5 + 10)); // 주말/공휴일 고려 여유분
  
  const fmt = d => d.toISOString().slice(0, 10).replace(/-/g, '');
  const url = `https://api.stock.naver.com/chart/domestic/item/${code}/day?startDateTime=${fmt(start)}&endDateTime=${fmt(end)}`;
  
  try {
    const data = await fetchJSON(url);
    if (!Array.isArray(data)) return [];
    return data.map(d => ({
      date: d.localDate,
      close: d.closePrice,
      volume: d.accumulatedTradingVolume,
      high: d.highPrice,
      low: d.lowPrice,
    }));
  } catch {
    return [];
  }
}

// ============ 스크리닝 로직 ============

// 이동평균선 위에 종가가 있는지 + 이평선 자체가 상승 중인지 체크
function checkAboveMA(dailyData, maDays) {
  // 이평선 기울기 확인을 위해 20일 전 MA도 필요 → maDays + 20일 데이터
  if (dailyData.length < maDays + 20) return null;
  
  // 현재 MA
  const recent = dailyData.slice(-maDays);
  const sum = recent.reduce((s, d) => s + d.close, 0);
  const ma = Math.round(sum / maDays);
  const lastClose = recent[recent.length - 1].close;
  
  if (lastClose <= ma) return null;
  
  // 20일 전 MA
  const older = dailyData.slice(-(maDays + 20), -20);
  const olderSum = older.reduce((s, d) => s + d.close, 0);
  const olderMa = Math.round(olderSum / maDays);
  
  // 이평선 상승 여부
  const maRising = ma > olderMa;
  const maSlope = ((ma / olderMa - 1) * 100).toFixed(2);  // 20일간 MA 변화율 (%)
  
  return {
    ma,
    ratio: ((lastClose / ma - 1) * 100).toFixed(1),  // 이평선 대비 괴리율 (%)
    maRising,
    maSlope,  // + = 상승, - = 하락
  };
}

function checkVolumeCondition(dailyData, config, stock) {
  if (dailyData.length < config.volumeDays + 1) return null;
  
  // 최근 N+1일 데이터 (오늘 포함)
  const recent = dailyData.slice(-(config.volumeDays + 1));
  
  // 조건 1: 전일 대비 거래량 100% 이상
  const today = recent[recent.length - 1];
  const yesterday = recent[recent.length - 2];
  
  if (!yesterday.volume || yesterday.volume === 0) return null;
  
  const volRatio = ((today.volume / yesterday.volume) - 1) * 100;
  if (volRatio < config.minVolRatio) return null;
  
  // 조건 2: 거래대금 & 회전율 (시총 대비)
  const closePrice = today.close || parseInt((stock.closePrice || '0').replace(/,/g, ''));
  const tradingValue = (today.volume * closePrice) / 100000000; // 억원
  const turnover = stock.marketValue > 0 ? (tradingValue / stock.marketValue) * 100 : 0; // %
  
  if (tradingValue < config.minTradingValue) return null;
  if (turnover < config.minTurnover) return null;
  
  // 조건 3: 최근 N일 거래량 증가 추세
  const volDays = recent.slice(-config.volumeDays);
  let increasing = true;
  for (let i = 1; i < volDays.length; i++) {
    if (volDays[i].volume <= volDays[i - 1].volume) {
      increasing = false;
      break;
    }
  }
  
  if (!increasing) return null;
  
  return {
    todayVol: today.volume,
    yesterdayVol: yesterday.volume,
    volRatio: volRatio.toFixed(1),
    tradingValue: Math.round(tradingValue),  // 거래대금 (억원)
    turnover: turnover.toFixed(2),           // 회전율 (%)
    volumes: volDays.map(d => d.volume),
    dates: volDays.map(d => d.date),
  };
}

// ============ 메인 ============

async function main() {
  const config = parseArgs();
  const startTime = Date.now();
  
  console.log('🔍 주식 조건 검색 스크리너');
  console.log('========================');
  console.log(`  PBR: ${config.maxPBR}배 이하`);
  console.log(`  시총: ${(config.minMarketCap / 10000).toFixed(1)}조원 이상`);
  console.log(`  거래량: 전일 대비 ${config.minVolRatio}% 이상 증가`);
  console.log(`  회전율: 시총 대비 ${config.minTurnover}% 이상`);
  console.log(`  거래대금: ${config.minTradingValue}억원 이상`);
  if (config.aboveMA > 0) {
    let maDesc = `${config.aboveMA}일 이동평균 위 종가`;
    if (config.maRisingOnly) maDesc += ' + 이평선 상승 중';
    console.log(`  이평선: ${maDesc}`);
  }
  console.log(`  거래량 추세: 최근 ${config.volumeDays}일 연속 증가`);
  console.log(`  시장: ${config.markets.join(', ')}`);
  console.log('');
  
  // 1단계: 전종목 리스트 (시총 기준 필터)
  console.log('📊 1단계: 종목 리스트 로딩...');
  let allStocks = [];
  
  for (const market of config.markets) {
    const stocks = await fetchStockList(market, config.pageSize);
    const filtered = stocks.filter(s => s.marketValue >= config.minMarketCap);
    console.log(`  ${market}: ${stocks.length}개 중 시총 ${(config.minMarketCap / 10000).toFixed(1)}조+ → ${filtered.length}개`);
    allStocks = allStocks.concat(filtered);
  }
  
  console.log(`  총 ${allStocks.length}개 종목 대상\n`);
  
  // 2단계: PBR 필터
  console.log('📊 2단계: PBR 필터링...');
  const pbrFiltered = [];
  let checked = 0;
  
  for (const stock of allStocks) {
    try {
      const detail = await fetchStockDetail(stock.code);
      checked++;
      
      if (checked % 20 === 0) {
        process.stdout.write(`  진행: ${checked}/${allStocks.length}\r`);
      }
      
      if (detail.pbr && detail.pbr > 0 && detail.pbr <= config.maxPBR) {
        pbrFiltered.push({
          ...stock,
          pbr: detail.pbr,
          per: detail.per,
          dividendYield: detail.dividendYield,
        });
      }
      
      await sleep(config.requestDelay);
    } catch (e) {
      // 개별 종목 에러는 무시
    }
  }
  
  console.log(`  PBR ${config.maxPBR}배 이하: ${pbrFiltered.length}개\n`);
  
  // 3단계: 거래량 조건 체크
  console.log('📊 3단계: 거래량 조건 필터링...');
  const results = [];
  checked = 0;
  
  for (const stock of pbrFiltered) {
    try {
      const chartDays = Math.max(config.volumeDays + 2, config.aboveMA + 30);
      const daily = await fetchDailyChart(stock.code, chartDays);
      checked++;
      
      if (checked % 10 === 0) {
        process.stdout.write(`  진행: ${checked}/${pbrFiltered.length}\r`);
      }
      
      // 이평선 체크
      if (config.aboveMA > 0) {
        const maResult = checkAboveMA(daily, config.aboveMA);
        if (!maResult) continue;
        if (config.maRisingOnly && !maResult.maRising) continue;
        stock._ma = maResult;
      }
      
      const volResult = checkVolumeCondition(daily, config, stock);
      if (volResult) {
        if (stock._ma) {
          volResult.ma200 = stock._ma.ma;
          volResult.maRatio = stock._ma.ratio;
          volResult.maRising = stock._ma.maRising;
          volResult.maSlope = stock._ma.maSlope;
        }
        results.push({
          ...stock,
          ...volResult,
        });
      }
      
      await sleep(config.requestDelay);
    } catch (e) {
      // 개별 종목 에러는 무시
    }
  }
  
  // 결과 정렬 (거래량 증가율 높은 순)
  results.sort((a, b) => parseFloat(b.volRatio) - parseFloat(a.volRatio));
  const topResults = results.slice(0, config.top);
  
  const elapsed = ((Date.now() - startTime) / 1000).toFixed(1);
  
  // ============ 출력 ============
  if (config.outputJson) {
    const now = new Date();
    const dateStr = now.toLocaleDateString('ko-KR', { timeZone: 'Asia/Seoul', year: 'numeric', month: '2-digit', day: '2-digit' })
      .replace(/\. /g, '-').replace(/\.$/, '').trim(); // "2026-02-25" 형식
    console.log(JSON.stringify({
      timestamp: now.toISOString(),
      date: dateStr,
      config: {
        maxPBR: config.maxPBR,
        minMarketCap: config.minMarketCap,
        minVolRatio: config.minVolRatio,
        minTurnover: config.minTurnover,
        minTradingValue: config.minTradingValue,
        aboveMA: config.aboveMA,
        volumeDays: config.volumeDays,
      },
      totalScanned: allStocks.length,
      pbrFiltered: pbrFiltered.length,
      matched: results.length,
      elapsed: `${elapsed}s`,
      results: topResults,
    }, null, 2));
  } else {
    console.log('');
    console.log('═══════════════════════════════════════════════════════════');
    console.log(`🎯 검색 결과: ${results.length}개 종목 매칭 (${elapsed}초 소요)`);
    console.log('═══════════════════════════════════════════════════════════');
    
    if (topResults.length === 0) {
      console.log('\n  조건에 맞는 종목이 없습니다.');
      console.log('  PBR 상한을 높이거나 (--pbr 1.5) 거래량 조건을 낮춰보세요 (--vol-ratio 50)');
    } else {
      console.log('');
      console.log(` ${'#'.padStart(3)} | ${'종목명'.padEnd(14)} | ${'PBR'.padStart(5)} | ${'시총(억)'.padStart(10)} | ${'거래량↑%'.padStart(8)} | ${'3일거래량추이'}`);
      console.log(' ' + '-'.repeat(85));
      
      topResults.forEach((r, i) => {
        const volTrend = r.volumes.map(v => formatVolume(v)).join(' → ');
        console.log(
          ` ${String(i + 1).padStart(3)} | ${r.name.padEnd(12)} | ${String(r.pbr).padStart(5)} | ${formatNumber(r.marketValue).padStart(10)} | ${('+' + r.volRatio + '%').padStart(8)} | ${volTrend}`
        );
      });
    }
    
    console.log('');
    console.log(`📅 ${new Date().toLocaleString('ko-KR', { timeZone: 'Asia/Seoul' })}`);
    console.log(`📊 스캔: ${allStocks.length} → PBR필터: ${pbrFiltered.length} → 최종: ${results.length}`);
  }
}

function formatNumber(n) {
  return n.toString().replace(/\B(?=(\d{3})+(?!\d))/g, ',');
}

function formatVolume(v) {
  if (v >= 1000000) return (v / 1000000).toFixed(1) + 'M';
  if (v >= 1000) return (v / 1000).toFixed(0) + 'K';
  return String(v);
}

main().catch(e => {
  console.error('❌ 오류:', e.message);
  process.exit(1);
});
