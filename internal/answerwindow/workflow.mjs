// Produces an explicit human-authored request; submitting still uses the normal
// Room composer and its role, identity and task checks.
export function workflowMessage(steps) {
  if (!steps.length || steps.length > 16) throw new Error('请安排 1–16 个步骤');
  const ids = new Set();
  for (const step of steps) {
    if (!/^[a-z][a-z0-9_.-]{0,31}$/.test(step.id) || ids.has(step.id)) throw new Error('步骤 ID 需唯一，以小写字母开头');
    if (!step.agent || !step.prompt.trim()) throw new Error('每个步骤都需要成员和工作内容');
    ids.add(step.id);
  }
  for (const step of steps) {
    if (new Set(step.dependsOn).size !== step.dependsOn.length || step.dependsOn.some(id => !ids.has(id) || id === step.id)) throw new Error('依赖必须指向其他步骤，且不能重复');
  }
  const done = new Set();
  while (done.size < steps.length) {
    const ready = steps.find(step => !done.has(step.id) && step.dependsOn.every(id => done.has(id)));
    if (!ready) throw new Error('依赖中存在循环，请调整');
    done.add(ready.id);
  }
  return '/team ' + JSON.stringify({steps}, null, 2);
}

export function setupWorkflow({getState, getComposer, isSending}) {
  const dialog = document.getElementById('workflow-dialog');
  const rows = document.getElementById('workflow-steps');
  const error = document.getElementById('workflow-error');
  let agents = [], draft = null, sequence = 0;
  function addRow(step = {}) {
    if (rows.children.length >= 16) { error.textContent = '最多 16 个步骤'; return; }
    const row = document.createElement('fieldset');
    const legend = document.createElement('legend'); legend.textContent = '协作步骤'; row.append(legend);
    function field(label, node, key) {
      const wrap = document.createElement('label'); wrap.textContent = label;
      node.dataset.field = key; wrap.append(node); row.append(wrap); return node;
    }
    const id = field('步骤 ID', document.createElement('input'), 'id');
    id.value = step.id || 'step-' + (++sequence); id.maxLength = 32; id.required = true;
    const member = field('执行成员', document.createElement('select'), 'agent');
    for (const agent of agents) member.add(new Option(agent.name + ' · ' + agent.id, agent.id));
    if (step.agent) member.value = step.agent;
    const prompt = field('本步骤工作内容', document.createElement('textarea'), 'prompt');
    prompt.value = step.prompt || ''; prompt.required = true; prompt.maxLength = 4000;
    const depends = field('等待哪些步骤完成（填步骤 ID，以逗号分隔；留空即可直接执行）', document.createElement('input'), 'dependsOn');
    depends.value = (step.dependsOn || []).join(', ');
    const remove = document.createElement('button'); remove.type = 'button'; remove.textContent = '移除此步骤'; remove.onclick = () => row.remove(); row.append(remove);
    rows.append(row);
  }
  document.getElementById('workflow-open').onclick = () => {
    const state = getState(); draft = getComposer();
    if (!draft || draft.disabled || isSending() || state?.connected === false || state?.userRole !== 'roommate') return;
    agents = state.agents || []; if (!agents.length) return;
    rows.replaceChildren(); error.textContent = ''; sequence = 0;
    try {
      const value = draft.value.trim();
      if (value.startsWith('/team ')) {
        const plan = JSON.parse(value.slice(6));
        workflowMessage(plan.steps); sequence = plan.steps.length;
        for (const step of plan.steps) addRow(step);
      } else {
        addRow({id:'develop', agent:agents[0].id, prompt:draft.value});
        addRow({id:'review', agent:(agents[1] || agents[0]).id, prompt:'检查开发结果，指出问题与验证结论。', dependsOn:['develop']});
      }
    } catch { rows.replaceChildren(); addRow(); error.textContent = '原草稿未改变，请重新配置协作步骤'; }
    document.getElementById('workflow-advanced').open = draft.value.trim().startsWith('/team {');
    document.getElementById('workflow-goal').value = draft.value.trim().replace(/^\/team\s+(?!\{)/,'');
    document.getElementById('workflow-natural-error').textContent = '';
    dialog.showModal();
  };
  document.getElementById('workflow-natural-cancel').onclick = () => dialog.close();
  document.getElementById('workflow-natural-form').onsubmit = event => {
    event.preventDefault();
    const error = document.getElementById('workflow-natural-error');error.textContent='';
    const state=getState();
    if(draft!==getComposer() || draft.disabled || isSending() || state?.connected===false || state?.userRole!=='roommate'){error.textContent='当前不能安排工作，请关闭后重试';return;}
    const text=document.getElementById('workflow-goal').value.trim();
    if(!text){error.textContent='请描述协作要求';return;}
    const message='/team '+text;
    if(draft.maxLength>0 && message.length>draft.maxLength){error.textContent='协作要求过长，请精简';return;}
    draft.value=message;draft.dispatchEvent(new Event('input',{bubbles:true}));dialog.close();draft.focus();
  };
  document.getElementById('workflow-add').onclick = () => addRow();
  document.getElementById('workflow-cancel').onclick = () => dialog.close();
  document.getElementById('workflow-form').onsubmit = event => {
    event.preventDefault(); error.textContent = '';
    try {
      const state = getState();
      if (draft !== getComposer() || draft.disabled || isSending() || state?.connected === false || state?.userRole !== 'roommate') throw new Error('当前不能安排工作，请关闭后重试');
      const steps = [...rows.children].map(row => {
        const value = key => row.querySelector('[data-field="'+key+'"]').value.trim();
        return {id:value('id'), agent:value('agent'), prompt:value('prompt'), dependsOn:value('dependsOn').split(/[,，\s]+/).filter(Boolean)};
      });
      const message = workflowMessage(steps);
      if (draft.maxLength > 0 && message.length > draft.maxLength) throw new Error('计划超出此输入框长度限制，请精简');
      draft.value = message; draft.dispatchEvent(new Event('input', {bubbles:true})); dialog.close(); draft.focus();
    } catch (e) { error.textContent = e.message; }
  };
}
