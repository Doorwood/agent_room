import {marked} from './vendor/marked.mjs';

// Use the lexer only. Model-provided HTML is never assigned to innerHTML.
export function renderMarkdown(container, source) {
 const make=(tag,text)=>{const node=document.createElement(tag);if(text!==undefined)node.textContent=text;return node;};
 function inline(parent,tokens) {
  for(const token of tokens || []) {
   if(['strong','em','del'].includes(token.type)){const node=make(token.type);inline(node,token.tokens);parent.append(node);}
   else if(token.type==='codespan')parent.append(make('code',token.text));
   else if(token.type==='br')parent.append(make('br'));
   else if(token.type==='link'){
    let safe=false;try {safe=['https:','http:','mailto:'].includes(new URL(token.href).protocol);}catch{}
    const node=make(safe?'a':'span');if(safe){node.href=token.href;node.target='_blank';node.rel='noopener noreferrer';}
    inline(node,token.tokens);parent.append(node);
   }else if(token.type==='image')parent.append(document.createTextNode(token.text || '[图片]'));
   else if(token.tokens)inline(parent,token.tokens);
   else parent.append(document.createTextNode(token.text ?? token.raw ?? ''));
  }
 }
 function blocks(parent,tokens){
  for(const token of tokens){
   if(token.type==='space' || token.type==='def')continue;
   if(token.type==='code'){
    const box=make('div');box.className='code-block';const bar=make('div');bar.className='code-toolbar';
    const button=make('button','复制代码');button.type='button';button.addEventListener('click',async()=>{try{await navigator.clipboard.writeText(token.text);button.textContent='已复制';}catch{button.textContent='复制失败，请选中代码复制';}});
    bar.append(make('span',token.lang || '代码'),button);const pre=make('pre');pre.append(make('code',token.text));box.append(bar,pre);parent.append(box);
   }else if(token.type==='table'){
    const scroll=make('div');scroll.className='table-scroll';const table=make('table');const head=make('thead');const header=make('tr');
    for(const cell of token.header){const th=make('th');inline(th,cell.tokens);header.append(th);}head.append(header);table.append(head);
    const body=make('tbody');for(const row of token.rows){const tr=make('tr');for(const cell of row){const td=make('td');inline(td,cell.tokens);tr.append(td);}body.append(tr);}table.append(body);scroll.append(table);parent.append(scroll);
   }else if(token.type==='list'){
    const list=make(token.ordered?'ol':'ul');if(token.ordered && token.start)list.start=token.start;
    for(const item of token.items){const li=make('li');if(item.task)li.append(document.createTextNode(item.checked?'☑ ':'☐ '));blocks(li,item.tokens);list.append(li);}parent.append(list);
   }else if(token.type==='blockquote'){const node=make('blockquote');blocks(node,token.tokens);parent.append(node);}
   else if(token.type==='hr')parent.append(make('hr'));
   else if(token.type==='heading'){const node=make('h'+Math.min(6,Math.max(1,token.depth)));inline(node,token.tokens);parent.append(node);}
   else if(token.type==='paragraph' || token.type==='text'){const node=make('p');if(token.tokens)inline(node,token.tokens);else node.textContent=token.text;parent.append(node);}
   else parent.append(make('p',token.text ?? token.raw ?? ''));
  }
 }
 const fragment=document.createDocumentFragment();
 try{blocks(fragment,marked.lexer(source,{gfm:true}));container.replaceChildren(fragment);}
 catch{container.textContent=source;}
}
