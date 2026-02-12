/**
 * 종목 추천 피커 (장마감 전 3시용)
 * 
 * 조건:
 *  1. 시가총액 상위 (기본: 5,000억 이상)
 *  2. 당일 거래량 상위
 *  3. 최근 5일 상승 추세 (종가 기준)
 *  4. 당일 양봉 (종가 > 시가)
 * 
 * 결과: 종합 점수 TOP 3 종목 추천
 * 
 * 사용법: node picker.js [--top 3] [--market-cap 5000] [--json]
 */

const https = require('https');

// ============ 설정 ============
const DEFAULT_CONFIG = {
  minMarketCap: 5000,     // 최소 시총 (억원)
  top: 3,                 // 추천 종목 수
  trendDays: 5,           // 상승 추세 확인 일수
  markets: ['KOSPI', 'KOSDAQ'],
  pageSize: 100,
  requestDelay: 60,
  outputJson: false,
};

// ============ CLI ============
function parseArgs() {
  const args = process.argv.slice(2);
  const config = { ...DEFAULT_CONFIG };
  for (let i = 0; i < args.length; i++) {
    switch (args[i]) {
      case '--top': config.top = parseInt(args[++i]); break;
      case '--market-cap': config.minMarketCap = parseInt(args[++i]); break;
      case '--days': config.trendDays = parseInt(args[++i]); break;
      case '--market': config.markets = [args[++i].toUpperCase()]; break;
      case '--json': config.outputJson = true; break;
      case '--delay': config.requestDelay = parseInt(args[++i]); break;
      case '--help':
        console.log(`
종목 추천 피커 (장마감 전 3시용)
================================
사용법: node picker.js [옵션]

옵션:
  --top <개>         추천 종목 수 (기본: 3)
  --market-cap <억>  최소 시가총액 (기본: 5000억)
  --days <일>        상승추세 확인 일수 (기본: 5)
  --market <시장>    KOSPI 또는 KOSDAQ (기본: 둘 다)
  --delay <ms>       API 호출 딜레이 (기본: 60ms)
  --json             JSON 출력
  --help             도움말
        `);
        process.exit(0);
    }
  }
  return config;
}

// ============ HTTP ============
function fetchJSON(url) {
  return new Promise((resolve, reject) => {
    https.get(url, {
      headers: { 'User-Agent': 'Mozilla/5.0', 'Accept': 'application/json' },
      timeout: 10000,
    }, (res) => {
      let data = '';
      res.on('data', c => data += c);
      res.on('end', () => {
        try { resolve(JSON.parse(data)); } catch(e) { reject(e); }
      });
    }).on('error', reject);
  });
}
const sleep = ms => new Promise(r => setTimeout(r, ms));

// ============ 네이버 API ============

async function fetchStockList(market, minMarketCap, pageSize) {
  const stocks = [];
  let page = 1;
  while (true) {
    const data = await fetchJSON(`https://m.stock.naver.com/api/stocks/marketValue/${market}?page=${page}&pageSize=${pageSize}`);
    if (!data.stocks || !data.stocks.length) break;

    for (const s of data.stocks) {
      const mv = parseInt((s.marketValue || '0').replace(/,/g, ''));
      if (mv < minMarketCap) break;
      
      const vol = parseInt((s.accumulatedTradingVolume || '0').replace(/,/g, ''));
      const fluct = parseFloat(s.fluctuationsRatio || '0');
      
      stocks.push({
        code: s.itemCode,
        name: s.stockName,
        market,
        marketValue: mv,
        closePrice: s.closePrice,
        volume: vol,
        fluctuation: fluct,
      });
    }

    const lastMv = parseInt((data.stocks[data.stocks.length - 1].marketValue || '0').replace(/,/g, ''));
    if (lastMv < minMarketCap) break;
    page++;
    await sleep(30);
  }
  return stocks;
}

async function fetchDailyChart(code, days) {
  const end = new Date();
  const start = new Date();
  start.setDate(start.getDate() - (days + 10));
  const fmt = d => d.toISOString().slice(0, 10).replace(/-/g, '');
  
  try {
    const data = await fetchJSON(
      `https://api.stock.naver.com/chart/domestic/item/${code}/day?startDateTime=${fmt(start)}&endDateTime=${fmt(end)}`
    );
    if (!Array.isArray(data)) return [];
    return data.map(d => ({
      date: d.localDate,
      close: d.closePrice,
      open: d.openPrice,
      high: d.highPrice,
      low: d.lowPrice,
      volume: d.accumulatedTradingVolume,
    }));
  } catch {
    return [];
  }
}

async function fetchStockDetail(code) {
  try {
    const data = await fetchJSON(`https://m.stock.naver.com/api/stock/${code}/integration`);
    const result = {};
    if (data.totalInfos) {
      for (const info of data.totalInfos) {
        switch (info.code) {
          case 'pbr': result.pbr = parseFloat((info.value || '0').replace(/[^0-9.]/g, '')); break;
          case 'per': result.per = parseFloat((info.value || '0').replace(/[^0-9.]/g, '')); break;
          case 'foreignRate': result.foreignRate = info.value; break;
        }
      }
    }
    // 수급 정보
    if (data.dealTrendInfos && data.dealTrendInfos.length > 0) {
      const recent = data.dealTrendInfos[0];
      result.foreignBuy = recent.foreignerPureBuyQuant;
      result.organBuy = recent.organPureBuyQuant;
    }
    return result;
  } catch {
    return {};
  }
}

// ============ 점수 계산 ============

function calculateScore(stock, daily, detail) {
  let score = 0;
  const reasons = [];

  // 1. 거래량 점수 (거래대금 기준, 상위일수록 높음)
  // volume은 이미 있음 - 거래량 많을수록 좋음
  const volScore = Math.min(stock.volume / 1000000, 30); // 최대 30점
  score += volScore;
  
  // 2. 시총 점수 (크면 안정적)
  const capScore = Math.min(stock.marketValue / 10000, 15); // 1조당 1점, 최대 15점
  score += capScore;

  // 3. 상승 추세 점수
  if (daily.length >= 3) {
    const recent = daily.slice(-5);
    let upDays = 0;
    let totalGain = 0;
    
    for (let i = 1; i < recent.length; i++) {
      if (recent[i].close > recent[i - 1].close) {
        upDays++;
        totalGain += (recent[i].close - recent[i - 1].close) / recent[i - 1].close * 100;
      }
    }
    
    // 상승일 비율 (최대 20점)
    const trendScore = (upDays / (recent.length - 1)) * 20;
    score += trendScore;
    
    // 누적 상승률 (최대 15점)
    const gainScore = Math.min(Math.max(totalGain, 0), 15);
    score += gainScore;
    
    if (upDays >= 3) reasons.push(`${upDays}일 연속 상승`);
    if (totalGain > 3) reasons.push(`5일 +${totalGain.toFixed(1)}%`);
  }

  // 4. 당일 양봉 & 상승률
  if (stock.fluctuation > 0) {
    score += Math.min(stock.fluctuation * 2, 10); // 최대 10점
    reasons.push(`오늘 +${stock.fluctuation}%`);
  }

  // 5. 거래량 급증 (최근 5일 평균 대비)
  if (daily.length >= 5) {
    const avgVol = daily.slice(-6, -1).reduce((a, b) => a + b.volume, 0) / 5;
    const todayVol = daily[daily.length - 1]?.volume || stock.volume;
    if (avgVol > 0) {
      const volRatio = todayVol / avgVol;
      if (volRatio > 1.5) {
        const volBonusScore = Math.min((volRatio - 1) * 5, 10); // 최대 10점
        score += volBonusScore;
        reasons.push(`거래량 ${(volRatio).toFixed(1)}배`);
      }
    }
  }

  // 6. 외국인/기관 매수 보너스
  if (detail.foreignBuy) {
    const fb = parseInt(detail.foreignBuy.replace(/[^0-9-]/g, ''));
    if (fb > 0) { score += 3; reasons.push('외국인 순매수'); }
  }
  if (detail.organBuy) {
    const ob = parseInt(detail.organBuy.replace(/[^0-9-]/g, ''));
    if (ob > 0) { score += 3; reasons.push('기관 순매수'); }
  }

  return { score: Math.round(score * 10) / 10, reasons };
}

// ============ 메인 ============

async function main() {
  const config = parseArgs();
  const startTime = Date.now();

  console.log('🎯 종목 추천 피커 (장마감 전)');
  console.log('============================');
  console.log(`  시총: ${(config.minMarketCap / 10000).toFixed(1)}조+ | 추세: ${config.trendDays}일`);
  console.log(`  시장: ${config.markets.join(', ')} | TOP ${config.top}`);
  console.log('');

  // 1단계: 전종목 로드
  console.log('📊 1단계: 종목 리스트...');
  let allStocks = [];
  for (const market of config.markets) {
    const stocks = await fetchStockList(market, config.minMarketCap, config.pageSize);
    console.log(`  ${market}: ${stocks.length}개 (시총 ${(config.minMarketCap/10000).toFixed(1)}조+)`);
    allStocks = allStocks.concat(stocks);
  }

  // 1차 필터: 당일 양봉 + 거래량 상위 50%
  allStocks.sort((a, b) => b.volume - a.volume);
  const volumeFiltered = allStocks
    .filter(s => s.fluctuation > 0)
    .slice(0, Math.max(Math.floor(allStocks.length * 0.4), 50));
  
  console.log(`  양봉+거래량 상위: ${volumeFiltered.length}개\n`);

  // 2단계: 상세 분석
  console.log('📊 2단계: 추세 & 수급 분석...');
  const candidates = [];
  let checked = 0;

  for (const stock of volumeFiltered) {
    try {
      const [daily, detail] = await Promise.all([
        fetchDailyChart(stock.code, config.trendDays + 3),
        fetchStockDetail(stock.code),
      ]);
      
      checked++;
      if (checked % 10 === 0) process.stdout.write(`  진행: ${checked}/${volumeFiltered.length}\r`);

      // 상승 추세 필터: 최근 데이터에서 상승일이 과반
      if (daily.length >= 3) {
        const recent = daily.slice(-(config.trendDays));
        let upDays = 0;
        for (let i = 1; i < recent.length; i++) {
          if (recent[i].close > recent[i - 1].close) upDays++;
        }
        
        // 최소 과반 이상 상승
        if (upDays >= Math.floor((recent.length - 1) / 2)) {
          const { score, reasons } = calculateScore(stock, daily, detail);
          candidates.push({
            ...stock,
            ...detail,
            score,
            reasons,
            upDays,
            dailyData: daily.slice(-config.trendDays),
          });
        }
      }

      await sleep(config.requestDelay);
    } catch (e) {}
  }

  // 점수순 정렬
  candidates.sort((a, b) => b.score - a.score);
  const topPicks = candidates.slice(0, config.top);

  const elapsed = ((Date.now() - startTime) / 1000).toFixed(1);

  // ============ 출력 ============
  if (config.outputJson) {
    console.log(JSON.stringify({
      timestamp: new Date().toISOString(),
      config: {
        minMarketCap: config.minMarketCap,
        trendDays: config.trendDays,
        top: config.top,
      },
      totalScanned: allStocks.length,
      candidates: candidates.length,
      elapsed: `${elapsed}s`,
      picks: topPicks.map(p => ({
        code: p.code,
        name: p.name,
        market: p.market,
        score: p.score,
        closePrice: p.closePrice,
        marketValue: p.marketValue,
        volume: p.volume,
        fluctuation: p.fluctuation,
        pbr: p.pbr,
        per: p.per,
        foreignBuy: p.foreignBuy,
        organBuy: p.organBuy,
        reasons: p.reasons,
        recentPrices: p.dailyData?.map(d => ({ date: d.date, close: d.close, volume: d.volume })),
      })),
    }, null, 2));
  } else {
    console.log('');
    console.log('═══════════════════════════════════════════════════════════════');
    console.log(`🎯 오늘의 추천 종목 TOP ${config.top} (${elapsed}초 소요)`);
    console.log('═══════════════════════════════════════════════════════════════');

    if (topPicks.length === 0) {
      console.log('\n  오늘은 조건에 맞는 종목이 없습니다.');
    } else {
      topPicks.forEach((p, i) => {
        const trend = p.dailyData ? p.dailyData.map(d => d.close).join(' → ') : '';
        console.log('');
        console.log(` 🏆 ${i + 1}위: ${p.name} (${p.code}) | ${p.market}`);
        console.log(`    점수: ${p.score}점 | 현재가: ${p.closePrice} | 등락: +${p.fluctuation}%`);
        console.log(`    시총: ${formatCap(p.marketValue)} | 거래량: ${formatVolume(p.volume)}`);
        if (p.pbr) console.log(`    PBR: ${p.pbr} | PER: ${p.per || 'N/A'}`);
        if (p.foreignBuy) console.log(`    외국인: ${p.foreignBuy} | 기관: ${p.organBuy}`);
        console.log(`    추천사유: ${p.reasons.join(', ')}`);
        if (trend) console.log(`    5일추이: ${trend}`);
      });
    }

    console.log('');
    console.log('─────────────────────────────────────────────────────────────');
    console.log(`📅 ${new Date().toLocaleString('ko-KR', { timeZone: 'Asia/Seoul' })}`);
    console.log(`📊 스캔: ${allStocks.length} → 양봉필터: ${volumeFiltered.length} → 추세통과: ${candidates.length} → TOP ${topPicks.length}`);
    console.log('');
    console.log('⚠️  본 추천은 기술적 지표 기반이며 투자 판단은 본인 책임입니다.');
  }
}

function formatCap(v) {
  if (v >= 10000) return (v / 10000).toFixed(1) + '조';
  return v.toLocaleString() + '억';
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
