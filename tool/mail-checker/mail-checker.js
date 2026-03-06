const { ImapFlow } = require('imapflow');
const fs = require('fs');
const { simpleParser } = require('mailparser');

const CONFIG_PATH = 'C:/Users/lab/.openclaw/mail-config.json';

async function checkMail() {
  const config = JSON.parse(fs.readFileSync(CONFIG_PATH, 'utf8'));
  const args = process.argv.slice(2);
  const jsonMode = args.includes('--json');
  const limitIdx = args.indexOf('--limit');
  const limit = limitIdx >= 0 ? parseInt(args[limitIdx + 1]) || 10 : 10;
  const unreadOnly = args.includes('--unread');
  const uidIdx = args.indexOf('--uid');
  const readUid = uidIdx >= 0 ? parseInt(args[uidIdx + 1]) : null;

  const client = new ImapFlow({
    host: config.imap.host,
    port: config.imap.port,
    secure: config.imap.secure,
    auth: config.imap.auth,
    logger: false,
    tls: { rejectUnauthorized: false }
  });

  await client.connect();
  const mailbox = await client.mailboxOpen('INBOX');
  const total = mailbox.exists;
  const unreadUids = await client.search({ unseen: true }, { uid: true });

  // --uid 옵션: 특정 메일 본문 읽기
  if (readUid) {
    let body = '';
    for await (const msg of client.fetch([readUid], { source: true }, { uid: true })) {
      const parsed = await simpleParser(msg.source);
      body = parsed.text || parsed.html?.replace(/<[^>]+>/g, '') || '(본문 없음)';
    }
    await client.logout();
    const preview = body.trim().slice(0, 2000);
    if (jsonMode) {
      console.log(JSON.stringify({ uid: readUid, body: preview }));
    } else {
      console.log(`\n📄 메일 본문 (UID: ${readUid})\n${'─'.repeat(50)}\n${preview}\n`);
    }
    return;
  }

  let targetUids;
  if (unreadOnly) {
    targetUids = unreadUids.slice(-limit).reverse();
  } else {
    const allUids = await client.search({ all: true }, { uid: true });
    targetUids = allUids.slice(-limit).reverse();
  }

  const mails = [];
  if (targetUids.length > 0) {
    for await (const msg of client.fetch(targetUids, { envelope: true, flags: true }, { uid: true })) {
      const env = msg.envelope;
      const from = env.from?.[0];
      mails.push({
        uid: msg.uid,
        subject: env.subject || '(제목없음)',
        from: from ? (from.name ? `${from.name} <${from.address}>` : from.address) : '?',
        date: env.date ? new Date(env.date).toLocaleString('ko-KR', { timeZone: 'Asia/Seoul' }) : '',
        unread: !msg.flags?.has('\\Seen'),
      });
    }
  }
  await client.logout();

  if (jsonMode) {
    console.log(JSON.stringify({ total, unreadCount: unreadUids.length, mails }, null, 2));
    return;
  }

  console.log(`\n📬 받은편지함 — 전체: ${total}개 / 읽지않음: ${unreadUids.length}개\n`);
  if (mails.length === 0) {
    console.log('메일이 없습니다.');
    return;
  }
  for (const m of mails) {
    const badge = m.unread ? '🔵 [미읽음]' : '   [읽음] ';
    console.log(`${badge} ${m.date}  (UID: ${m.uid})`);
    console.log(`   발신: ${m.from}`);
    console.log(`   제목: ${m.subject}`);
    console.log('');
  }
  console.log(`💡 본문 보기: node mail-checker.js --uid <UID번호>`);
}

checkMail().catch(e => { console.error('오류:', e.message); process.exit(1); });
