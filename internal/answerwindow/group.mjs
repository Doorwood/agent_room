// Project retained transcript events into one model card per host turn.
// Unknown turns remain separate; never infer task ownership from adjacency.
export function groupMessages(messages) {
  const cards = [];
  const turns = new Map();
  const prompts = new Map();
  for (const message of messages) {
    if (message.role === 'user' && message.turn && (message.kind === 'prompt' || message.kind === 'recovery-prompt')) prompts.set(message.turn, message);
  }
  function model(key, message, prompt) {
    const card = {key, seq:prompt?.seq || (message.kind==='progress'?0:message.seq), time:message.role==='assistant'?message.time:null, role:'assistant', author:'模型', owner:message.owner, turn:message.turn,
      text:'', progress:[], ack:message.taskStatus || prompt?.ack || '', kind:'task', final:false, working:!(/等待/.test(message.ack || ''))};
    cards.push(card);
    return card;
  }
  for (const message of messages) {
    if (message.role === 'user') {
      cards.push({...message, key:'user:' + message.seq});
      if (message.kind === 'prompt' || message.kind === 'recovery-prompt') {
        const key = message.turn ? 'turn:' + message.turn : 'pending:' + (message.clientId || message.seq);
        const card = model(key, message, message);
        if (message.turn) turns.set(message.turn, card);
      }
      continue;
    }
    const key = message.turn ? 'turn:' + message.turn : 'item:' + message.seq;
    let card = message.turn ? turns.get(message.turn) : null;
    if (!card) {
      card = model(key, message, prompts.get(message.turn));
      if (message.turn) turns.set(message.turn, card);
    }
    if (message.taskStatus) card.ack = message.taskStatus;
    if (message.kind === 'progress') {card.progress.push(message.text);if(!card.final)card.time=message.time;}
    else {
      if(!card.seq)card.seq=message.seq;
      card.time=message.time;
      card.text += (card.text ? '\n\n' : '') + message.text;
      card.final = true;
    }
  }
  for (const card of cards) {
    if (card.role !== 'assistant') continue;
    // Completion and exceptional states come from the durable prompt status.
    const terminal = /完成|失败|中断|跳过|需要确认|已继续处理/.test(card.ack);
    card.working = !card.final && !terminal && !/等待/.test(card.ack);card.queued=!card.final && !terminal && /等待/.test(card.ack);
    if (!card.final) card.text = terminal ? card.ack : card.progress.at(-1) || card.ack || '模型正在处理…';
  }
  return cards;
}
