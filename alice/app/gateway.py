"""Ingress-only UI and allowlisted manager API. No Supervisor token in browsers."""
import html
import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import re
from urllib.parse import urlsplit
import sys

sys.path.insert(0,'/app/manager')
from proxy import allowed_path


def heading_id(text, index):
    return 'section-'+str(index)


def markdown(text):
    # Escape source before adding our own allowlisted markup.
    def inline(value):
        value=html.escape(value)
        value=re.sub(r'\[([^\]]+)\]\(([A-Za-z0-9_-]+)\.md\)',r'<a href="wiki/\2">\1</a>',value)
        value=re.sub(r'\*\*([^*]+)\*\*',r'<strong>\1</strong>',value)
        return re.sub(r'`([^`]+)`',r'<code>\1</code>',value)
    def cells(line):
        return [cell.strip() for cell in line.strip().strip('|').split('|')]
    lines=[]; paragraph=[]; code=False; listing=False; headings=0; table=[]
    def close_table():
        if table:
            head,body=table[0],[row for row in table[1:] if not all(re.fullmatch(r':?-{3,}:?',cell) for cell in row)]
            lines.append('<div class="docs-table"><table><thead><tr>'+''.join('<th>'+inline(cell)+'</th>' for cell in head)+'</tr></thead><tbody>'+''.join('<tr>'+''.join('<td>'+inline(cell)+'</td>' for cell in row)+'</tr>' for row in body)+'</tbody></table></div>')
            table.clear()
    def flush():
        if paragraph:
            lines.append('<p>'+inline(' '.join(paragraph))+'</p>');paragraph.clear()
    for line in text.splitlines():
        if line.startswith('```'):
            flush()
            if listing:lines.append('</ul>');listing=False
            lines.append('</code></pre>' if code else '<pre><code>');code=not code
        elif code:lines.append(html.escape(line)+'\n')
        elif line.startswith('|'):
            flush()
            if listing:lines.append('</ul>');listing=False
            table.append(cells(line))
        else:
            close_table()
            match=re.match(r'^(#{1,4}) (.*)',line)
            if match:
                flush()
                if listing:lines.append('</ul>');listing=False
                headings+=1;level=len(match[1])
                lines.append(f'<h{level} id="{heading_id(match[2],headings)}">{inline(match[2])}</h{level}>')
            elif line.startswith('- '):
                flush()
                if not listing:lines.append('<ul>');listing=True
                lines.append('<li>'+inline(line[2:])+'</li>')
            elif line.startswith('  ') and listing:
                lines[-1]=lines[-1][:-5]+' '+inline(line.strip())+'</li>'
            elif not line.strip():
                flush()
                if listing:lines.append('</ul>');listing=False
            else:
                if listing:lines.append('</ul>');listing=False
                paragraph.append(line.strip())
    flush();close_table()
    if listing:lines.append('</ul>')
    if code:lines.append('</code></pre>')
    return '\n'.join(lines)


WIKI_PAGES=[
    ('Начало работы', [('Home','Обзор','Всё для первого запуска'),('Installation','Установка приложения','Установите и настройте приложение в HA'),('Compatibility','Совместимость','Модели, прошивки и требования'),('Bridge','Подключение колонки','SSH, ключ и установка моста')]),
    ('Каждый день', [('Commands','Голосовые команды','Фразы, автоматизации и устройства'),('Music','Музыка','Spotify и YouTube Music'),('LLM','Умные ответы','LLM и маршрутизация запросов')]),
    ('Обслуживание', [('Recovery','Обновление и восстановление','Резервные копии и решение проблем'),('Migration','Переход со старого моста','Переход с существующего менеджера'),('Security','Безопасность и данные','Доступ, ключи и приватные данные'),('Release','О проекте и релизах','Подготовка публичного выпуска')])]


def wiki_page(name, root):
    pages=[page for _,group in WIKI_PAGES for page in group]
    meta=next((page for page in pages if page[0]==name),(name,name,''))
    source=(root/(name+'.md')).read_text(encoding='utf-8')
    title=next((line[2:] for line in source.splitlines() if line.startswith('# ')),meta[1])
    nav=''
    for group,entries in WIKI_PAGES:
        nav+='<section class="docs-nav-group"><h2>'+group+'</h2>'
        for slug,label,description in entries:
            active=' aria-current="page"' if slug==name else ''
            nav+='<a href="wiki/'+slug+'"'+active+'>'+html.escape(label)+'</a>'
        nav+='</section>'
    search=json.dumps([{'slug':slug,'title':label,'description':desc,'text':(root/(slug+'.md')).read_text(encoding='utf-8')} for slug,label,desc in pages if (root/(slug+'.md')).is_file()],ensure_ascii=False).replace('<','\\u003c').replace('>','\\u003e').replace('&','\\u0026')
    toc=[];index=0
    for line in source.splitlines():
        match=re.match(r'^(#{1,4}) (.*)',line)
        if match:
            index+=1
            if len(match[1])>1:toc.append('<a href="wiki/'+name+'#'+heading_id(match[2],index)+'">'+html.escape(match[2])+'</a>')
    if name=='Home':
        cards=''
        for number,slug in enumerate(('Installation','Compatibility','Bridge','Commands'),1):
            _,label,description=next(page for page in pages if page[0]==slug)
            cards+='<a class="docs-task" href="wiki/'+slug+'"><span class="docs-task-number">0'+str(number)+'</span><h2>'+label+'</h2><p>'+description+'</p><span class="docs-task-arrow" aria-hidden="true">↗</span></a>'
        content='<div class="docs-hero"><h1>Справка</h1></div><div class="docs-tasks">'+cards+'</div><section class="docs-callout"><span aria-hidden="true">i</span><div><h2>Перед подключением</h2><p>Нужны Home Assistant OS с Supervisor и совместимая колонка с уже настроенным root SSH. Приложение устанавливает мост; способ подготовки колонки зависит от модели и прошивки.</p><a href="wiki/Compatibility">Проверить совместимость →</a></div></section><div class="docs-home-links"><a href="wiki/Recovery">Что-то не работает?<span>Обновление и восстановление →</span></a><a href="wiki/Migration">Уже пользуетесь мостом?<span>Переход со старого моста →</span></a></div>'
    else:
        content='<div class="docs-breadcrumb"><a href="wiki/Home">Справка</a><span>/</span>'+html.escape(meta[1])+'</div><article class="docs-article">'+markdown(source)+'</article>'
        position=next((i for i,p in enumerate(pages) if p[0]==name),0)
        pager=''
        for i,label in ((position-1,'← Предыдущая'),(position+1,'Следующая →')):
            if 0<=i<len(pages):pager+='<a href="wiki/'+pages[i][0]+'"><small>'+label+'</small><strong>'+html.escape(pages[i][1])+'</strong></a>'
        content+='<nav class="docs-pager" aria-label="Страницы справочника">'+pager+'</nav>'
    aside='<aside class="docs-toc"><span class="docs-eyebrow">На этой странице</span>'+''.join(toc)+'</aside>' if toc and name!='Home' else ''
    return '<style>'+WIKI_STYLE+'</style><div class="docs-layout"><aside class="docs-sidebar"><div class="docs-sidebar-title">Справка</div><label class="docs-search"><span aria-hidden="true">⌕</span><input id="docs-search" type="search" placeholder="Найти в справочнике" aria-label="Поиск по всем статьям" autocomplete="off"></label><div id="docs-search-results" hidden aria-live="polite"></div><details class="docs-mobile-nav"><summary>Разделы справочника</summary>'+nav+'</details><div class="docs-desktop-nav">'+nav+'</div><p class="docs-offline"><span>●</span> Доступно без интернета</p></aside><div class="docs-content">'+content+'</div>'+aside+'</div><script type="application/json" id="docs-index">'+search+'</script><script>'+WIKI_SCRIPT+'</script>'


WIKI_SCRIPT=r"""(()=>{const input=document.getElementById('docs-search'),box=document.getElementById('docs-search-results');if(!input||!box)return;const pages=JSON.parse(document.getElementById('docs-index').textContent);input.addEventListener('input',()=>{const query=input.value.trim().toLocaleLowerCase();box.replaceChildren();box.hidden=!query;if(!query)return;const results=pages.filter(p=>(p.title+' '+p.text).toLocaleLowerCase().includes(query)).slice(0,10);if(!results.length){const empty=document.createElement('p');empty.textContent='Ничего не найдено. Попробуйте другое слово.';box.append(empty);}for(const p of results){const link=document.createElement('a'),title=document.createElement('strong'),desc=document.createElement('span');link.href='wiki/'+p.slug;title.textContent=p.title;desc.textContent=p.description;link.append(title,desc);box.append(link);}});})();"""

WIKI_STYLE=r"""
.docs-layout{--docs-muted:var(--muted);--docs-line:var(--line);--docs-accent:var(--accent);display:grid;grid-template-columns:236px minmax(0,800px) 190px;gap:48px;max-width:1420px;margin:0 auto;padding:40px 32px 80px;color:var(--fg);font:15px/1.65 system-ui,sans-serif}.docs-layout *{box-sizing:border-box}.docs-layout a{color:inherit;text-decoration:none}.docs-layout a:focus-visible,.docs-layout input:focus-visible,.docs-layout summary:focus-visible{outline:2px solid var(--docs-accent);outline-offset:4px}.docs-sidebar{min-width:0;position:sticky;top:28px;align-self:start}.docs-sidebar-title{font-size:23px;font-weight:500;margin-bottom:24px}.docs-sidebar-title span{display:block;color:var(--docs-muted);font-size:12px;letter-spacing:1.8px;margin-top:5px}.docs-search{display:flex;gap:10px;align-items:center;background:var(--tint);border:1px solid var(--line);border-radius:11px;padding:10px 12px}.docs-search>span{font-size:22px;line-height:1;color:var(--docs-muted)}.docs-search input{color:inherit;background:none;border:0;min-width:0;width:100%;font:inherit;font-size:12px;padding:2px;outline:0}.docs-nav-group{margin:25px 0}.docs-nav-group h2{font-size:13px!important;font-weight:400;color:var(--docs-muted);margin:0 12px 10px!important;line-height:1.3}.docs-nav-group a{display:block;padding:8px 12px;border-radius:8px;font-size:13px;color:var(--muted);line-height:1.4;margin:3px 0}.docs-nav-group a:hover{background:var(--tint);color:var(--fg)}.docs-nav-group a[aria-current]{background:var(--tint);color:var(--accent);font-weight:650;box-shadow:inset 2px 0 var(--accent)}.docs-offline{margin:30px 12px 0;color:var(--docs-muted);font-size:12px}.docs-offline span{color:#77cdb7;font-size:12px;margin-right:6px}.docs-content{min-width:0;padding-top:8px}.docs-eyebrow{font-size:13px;font-weight:400;color:var(--accent)}.docs-hero h1{font-size:32px;font-weight:500;line-height:1.15;margin:18px 0 20px}.docs-hero>p{font-size:16px;color:var(--muted);max-width:570px;line-height:1.75;margin-bottom:30px}.docs-tasks{display:grid;grid-template-columns:1fr 1fr;gap:14px;margin-top:32px}.docs-task{position:relative;padding:24px!important;border:1px solid var(--tint);border-radius:16px;background:var(--bar);transition:border-color .2s}.docs-task:hover{border-color:var(--accent);}.docs-task-number{color:var(--accent);font-size:12px;letter-spacing:1px}.docs-task h2{font-size:17px;margin:18px 0 7px;line-height:1.3}.docs-task p{color:var(--docs-muted);font-size:13px;line-height:1.6;margin:0;max-width:230px}.docs-task-arrow{position:absolute;right:22px;top:20px;color:var(--muted);font-size:20px}.docs-callout{display:flex;gap:16px;margin-top:24px;padding:22px;border:1px solid var(--tint);border-radius:14px;background:var(--tint)}.docs-callout>span{border:1px solid var(--accent);color:var(--accent);border-radius:50%;width:20px;height:20px;text-align:center;font-size:12px;flex-shrink:0;margin-top:2px}.docs-callout h2{font-size:14px;margin:0 0 8px}.docs-callout p{font-size:13px;color:var(--muted);margin:0 0 10px}.docs-callout a{color:var(--accent);font-size:12px}.docs-home-links{display:grid;grid-template-columns:1fr 1fr;gap:24px;margin-top:30px}.docs-home-links>a{font-size:13px;color:var(--fg)}.docs-home-links span{display:block;font-size:12px;color:var(--docs-muted);margin-top:6px}.docs-breadcrumb{display:flex;gap:12px;font-size:12px;color:var(--docs-muted);margin-bottom:24px;flex-wrap:wrap}.docs-breadcrumb a:hover{color:var(--accent)}.docs-breadcrumb>span{color:var(--muted)}.docs-article{font-size:15px;line-height:1.85}.docs-article h1{font-size:36px;line-height:1.2;margin:0 0 28px;max-width:680px}.docs-article h2{font-size:22px;line-height:1.4;margin:42px 0 16px;padding-top:12px;border-top:1px solid var(--docs-line)}.docs-article h3{font-size:17px;line-height:1.5;margin-top:28px}.docs-article h1,.docs-article h2,.docs-article h3{scroll-margin-top:28px}.docs-article p{color:var(--muted);margin:0 0 18px}.docs-article ul{padding-left:22px;color:var(--muted);margin:15px 0 22px}.docs-article li{padding-left:5px;margin:9px 0}.docs-article a{color:var(--accent);text-decoration:underline;text-underline-offset:3px}.docs-article code{font-size:.86em;color:var(--accent);background:var(--tint);border:1px solid var(--tint);border-radius:5px;padding:2px 5px;overflow-wrap:anywhere}.docs-article pre{background:var(--tint);border:1px solid var(--tint);padding:20px;border-radius:12px;overflow:auto;line-height:1.65;margin:22px 0}.docs-article pre code{border:0;background:none;padding:0;white-space:pre;overflow-wrap:normal}.docs-toc{position:sticky;top:32px;align-self:start;border-left:1px solid var(--docs-line);padding:8px 0 8px 20px}.docs-toc>.docs-eyebrow{font-size:13px;color:var(--muted);display:block;margin-bottom:15px}.docs-toc a{display:block;font-size:12px;color:var(--docs-muted);margin:12px 0;line-height:1.6}.docs-toc a:hover{color:var(--accent)}.docs-pager{display:grid!important;grid-template-columns:1fr 1fr;gap:15px;margin-top:42px;padding:22px 0!important;border-top:1px solid var(--docs-line)}.docs-pager a{padding:16px;border:1px solid var(--docs-line);border-radius:12px;display:flex;flex-direction:column}.docs-pager a:hover{border-color:var(--tint)}.docs-pager small{font-size:12px;color:var(--docs-muted)}.docs-pager strong{font-size:13px;font-weight:550;margin-top:6px}.docs-mobile-nav{display:none}#docs-search-results{margin-top:8px;background:var(--tint);border:1px solid var(--line);border-radius:12px;max-height:420px;overflow:auto;padding:6px}#docs-search-results[hidden]{display:none}#docs-search-results a{display:block;padding:10px;border-radius:7px}#docs-search-results a:hover{background:var(--tint)}#docs-search-results strong{display:block;font-size:12px}#docs-search-results span,#docs-search-results p{font-size:12px;color:var(--docs-muted);line-height:1.5}#docs-search-results span{display:block;margin-top:4px}
@media(min-width:1500px){.docs-layout{gap:58px}}@media(max-width:1200px){.docs-layout{grid-template-columns:218px minmax(0,1fr);gap:36px}.docs-toc{display:none}}@media(max-width:760px){.docs-layout{display:block;padding:24px 20px 50px}.docs-sidebar{position:static}.docs-sidebar-title{font-size:20px;margin-bottom:16px}.docs-sidebar-title span{display:none}.docs-search{max-width:none}.docs-mobile-nav{display:block;border-bottom:1px solid var(--docs-line);margin:12px 0 26px}.docs-mobile-nav>summary{font-size:12px;color:var(--accent);padding:12px 0;cursor:pointer}.docs-desktop-nav,.docs-offline{display:none}.docs-content{padding:0}.docs-hero h1{font-size:34px;}.docs-hero>p{font-size:14px}.docs-task{padding:19px!important}.docs-task h2{font-size:15px}.docs-task p{font-size:12px}.docs-article h1{font-size:29px}.docs-article{font-size:14px}.docs-home-links{gap:18px}.docs-breadcrumb{font-size:12px}}@media(max-width:420px){.docs-tasks{grid-template-columns:1fr}.docs-task h2{margin-top:12px}.docs-home-links{grid-template-columns:1fr}.docs-pager{grid-template-columns:1fr}}
.docs-layout{max-width:1280px;font-family:Roboto,system-ui,-apple-system,"Segoe UI",sans-serif}
.docs-sidebar-title{font-size:20px}
.docs-search{border:0;border-radius:12px}
.docs-nav-group h2{font-size:11px!important;font-weight:500;letter-spacing:.08em;text-transform:uppercase}
.docs-nav-group a{border-radius:12px;font-size:14px;color:var(--fg);margin:2px 0}
.docs-nav-group a[aria-current]{background:color-mix(in srgb,var(--accent) 14%,transparent);color:var(--accent);font-weight:500;box-shadow:none}
.docs-hero h1,.docs-article h1{font-size:28px;font-weight:500}
.docs-article h2{font-size:20px;font-weight:500}.docs-article h3{font-size:16px;font-weight:500}
.docs-task{border:0;border-radius:20px;transition:box-shadow .2s}.docs-task:hover{box-shadow:0 0 0 2px var(--accent)}
.docs-task h2{font-weight:500}
.docs-callout,.docs-pager a,#docs-search-results{border:0;border-radius:20px;background:var(--bar)}
.docs-pager a:hover{box-shadow:0 0 0 2px var(--accent)}
.docs-article pre{border:0;border-radius:14px}
.docs-table{overflow-x:auto;margin:20px 0 24px}
.docs-table table{width:100%;border-collapse:separate;border-spacing:0 4px;font-size:14px;line-height:1.5}
.docs-table th{padding:4px 14px;text-align:left;font-size:12px;font-weight:500;color:var(--muted)}
.docs-table td{padding:12px 14px;background:var(--bar);color:var(--fg);vertical-align:top}
.docs-table td:first-child{border-radius:12px 0 0 12px;font-weight:500}.docs-table td:last-child{border-radius:0 12px 12px 0}
.docs-article strong{color:var(--fg);font-weight:500}
"""


class Handler(BaseHTTPRequestHandler):
    def log_message(self,*_):pass

    def reply(self,status,body,kind='application/json; charset=utf-8'):
        if not isinstance(body,bytes):body=json.dumps(body,ensure_ascii=False).encode()
        self.send_response(status)
        self.send_header('Content-Type',kind)
        self.send_header('Content-Length',str(len(body)))
        self.send_header('Cache-Control','no-store')
        self.send_header('X-Content-Type-Options','nosniff')
        self.send_header('Referrer-Policy','same-origin')
        self.end_headers();self.wfile.write(body)

    def do_GET(self):self._request()
    def do_POST(self):self._request()
    def do_PATCH(self):self._request()
    def do_PUT(self):self._request()
    def do_DELETE(self):self._request()

    def _request(self):
        if self.client_address[0]!=self.server.ingress_address:
            return self.reply(403,{'error':'Откройте приложение через Home Assistant.'})
        raw=self.path
        path=urlsplit(raw).path
        if raw=='/' and self.command=='GET':
            return self.reply(200,self.index(),'text/html; charset=utf-8')
        if self.command=='GET' and re.fullmatch(r'/wiki/[A-Za-z0-9_-]+',raw):
            name=raw.split('/')[-1]
            file=self.server.root/'wiki'/ (name+'.md')
            if not file.is_file():return self.reply(404,{'error':'Страница не найдена'})
            prefix=self.headers.get('X-Ingress-Path','')
            if not re.fullmatch(r'/api/hassio_ingress/[A-Za-z0-9._-]+/?',prefix):
                return self.reply(400,{'error':'Ingress path unavailable'})
            page=self.shell(wiki_page(name,self.server.root/'wiki'),prefix.rstrip('/')+'/')
            return self.reply(200,page,'text/html; charset=utf-8')
        allowed=allowed_path(raw,self.command)
        if not allowed:return self.reply(404,{'error':'Неизвестный маршрут'})
        bearer=self.headers.get('Authorization','')
        if not bearer.startswith('Bearer ') or not bearer[7:] or len(bearer)>10000:
            return self.reply(401,{'error':'Требуется вход в HA'})
        if self.headers.get('Transfer-Encoding') or len(self.headers.get_all('Content-Length',[]))>1:
            return self.reply(400,{'error':'Некорректный запрос'})
        try:
            length=int(self.headers.get('Content-Length','0'))
            if not 0<=length<=65536:return self.reply(413,{'error':'Запрос слишком большой'})
            self.connection.settimeout(20)
            body=self.rfile.read(length) if length else None
            if body is not None and len(body)!=length:return self.reply(400,{'error':'Неполный запрос'})
            conn=http.client.HTTPConnection('127.0.0.1',18085,timeout=20)
            try:
                conn.request(self.command,allowed,body,{'Authorization':bearer,'Content-Type':'application/json'})
                response=conn.getresponse();data=response.read(2*1024*1024+1)
                if len(data)>2*1024*1024 or 300<=response.status<400:raise ValueError()
                json.loads(data)
                return self.reply(response.status,data)
            finally:conn.close()
        except (OSError,ValueError,http.client.HTTPException):
            return self.reply(502,{'error':'Сервис колонок ещё запускается или недоступен'})

    def shell(self,body,base):
        overview='data-alice-overview' in body
        def tab(href,label,active):
            return '<a href="'+href+'"'+(' aria-current="page"' if active else '')+'>'+label+'</a>'
        dashboard=self.server.status.get('dashboard_path','alice-app') if isinstance(getattr(self.server,'status',None),dict) else 'alice-app'
        if not re.fullmatch(r'[a-z0-9-]+',str(dashboard)):dashboard='alice-app'
        navigation='<a href="/'+dashboard+'/speakers" target="_top">← В приложение</a>'+tab('wiki/Home','Справка',not overview)
        skip=html.escape((urlsplit(getattr(self,'path','/')).path.lstrip('/') or './')+'#content',quote=True)
        return ('<!doctype html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><base href="'+html.escape(base,quote=True)+'"><title>Алиса — '+('Обзор' if overview else 'Справка')+'</title><style>'+STYLE+'</style><script>'+THEME_SCRIPT+'</script></head><body class="'+('app-overview' if overview else 'app-docs')+'"><a class="skip-link" href="'+skip+'">К содержимому</a>'+('' if overview else '<header class="app-header"><a class="app-brand" href="./" aria-label="Алиса — главная"><span class="app-mark" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M4 10v4m4-8v12m4-15v18m4-15v12m4-8v4"/></svg></span><strong>Алиса</strong></a><nav class="app-tabs" aria-label="Приложение">'+navigation+'</nav><a class="app-help" href="wiki/Installation">С чего начать</a></header>')+'<main id="content" tabindex="-1">'+body+'</main></body></html>').encode()

    def index(self):
        prefix=self.headers.get('X-Ingress-Path','')
        if not re.fullmatch(r'/api/hassio_ingress/[A-Za-z0-9._-]+/?',prefix):
            return b'Ingress path unavailable'
        status=self.server.status
        notice=''
        if not status.get('configured') and not status.get('dashboard_ready'):
            notice='<div class="notice"><strong>Добро пожаловать! Подключим первую колонку.</strong><p>Сначала задайте локальный адрес Home Functions в настройках приложения, затем перезапустите его.</p><a href="wiki/Installation">Открыть пошаговую инструкцию →</a></div>'
        if not status.get('dashboard_ready'):
            notice+='<p class="notice">Панель подготавливается. Если сообщение не исчезает после обновления страницы, проверьте журнал приложения и Wiki.</p>'
        dashboard=status.get('dashboard_path','alice-app')
        if not re.fullmatch(r'[a-z0-9-]+',dashboard):dashboard='alice-app'
        target='/'+dashboard+'/speakers'
        body=notice+('<div data-alice-overview><div class="notice mobile-launch" hidden><strong>Открываем ваши колонки</strong>'
                     '<p>Панель откроется в текущем Home Assistant.</p><a id="open-dashboard" target="_top" href="'+target+'">Открыть панель →</a></div>'
                     '</div>')
        body+='<script>'+MOBILE_OVERVIEW_SCRIPT+'</script>'
        return self.shell(body,prefix.rstrip('/')+'/')


STYLE='''[hidden]{display:none!important}*{box-sizing:border-box}:root{color-scheme:light dark;--bg:light-dark(#fafafa,#111);--bar:light-dark(#fff,#1d1e21);--fg:light-dark(#212121,#e1e1e1);--muted:light-dark(#6b6b6b,#9b9b9b);--line:light-dark(#0000001f,#ffffff1f);--tint:light-dark(#0000000a,#ffffff0f);--accent:light-dark(#0288d1,#8ab4f8)}html{font-family:Roboto,system-ui,-apple-system,"Segoe UI",sans-serif;background:var(--bg);color:var(--fg)}body{margin:0}a{color:var(--accent)}a:focus-visible,button:focus-visible{outline:2px solid var(--accent);outline-offset:2px}.skip-link{position:absolute;top:-60px;left:12px;padding:12px;background:var(--bar);z-index:10}.skip-link:focus{top:8px}.app-header{height:60px;display:flex;align-items:center;gap:24px;padding:0 max(24px,calc((100vw - 1280px)/2));border-bottom:1px solid var(--line);background:var(--bar)}.app-brand{display:flex;align-items:center;gap:10px;color:inherit;text-decoration:none;flex-shrink:0}.app-brand strong{font-size:20px;font-weight:500}.app-mark{display:grid;place-items:center;width:24px;height:24px;color:var(--accent)}.app-mark svg{width:24px;height:24px}.app-tabs{display:flex;gap:4px;height:100%;align-items:center}.app-tabs a{position:relative;display:flex;align-items:center;height:100%;padding:0 14px;text-decoration:none;font-size:14px;font-weight:500;color:var(--muted)}.app-tabs a:hover{color:var(--fg)}.app-tabs a[aria-current]{color:var(--fg)}.app-tabs a[aria-current]:after{content:"";position:absolute;left:12px;right:12px;bottom:0;height:3px;border-radius:3px 3px 0 0;background:var(--accent)}.app-help{margin-left:auto;text-decoration:none;font-size:14px;color:var(--muted);white-space:nowrap}.app-help:hover{color:var(--fg)}.app-help span{display:none}main{outline:none}.notice{max-width:1232px;margin:16px auto;padding:12px 16px;background:var(--tint);border-radius:12px;font-size:14px;line-height:1.5}.notice p{color:var(--muted);margin:4px 0}.notice a{text-decoration:none}iframe{display:block;width:100%;height:calc(100dvh - 56px);border:0;background:transparent}.app-overview{height:100dvh;display:flex;flex-direction:column;overflow:hidden}.app-overview .app-header{flex-shrink:0}.app-overview main{flex:1;min-height:0;display:flex;flex-direction:column}.app-overview [data-alice-overview]{flex:1;min-height:0;display:flex;flex-direction:column}.app-overview iframe{flex:1;min-height:0;height:100%;width:100%}.app-overview:has(.notice){overflow:auto}.app-overview:has(.notice) iframe{height:calc(100dvh - 220px);min-height:420px}@media(max-width:760px){.app-header{padding:0 16px;gap:12px}.app-help{display:none}.app-tabs{margin-left:auto}.notice{margin:12px}}@media(prefers-reduced-motion:reduce){*,*::before,*::after{scroll-behavior:auto!important;transition:none!important}}'''


# The panel stores the colours of the active HA theme; help pages use them so both look the same.
THEME_SCRIPT=r"""(()=>{try{const t=JSON.parse(localStorage.getItem('alice-theme')||'null');if(!t)return;const r=document.documentElement.style,ok=v=>typeof v==='string'&&/^[#a-z0-9(),.%\s-]{1,60}$/i.test(v);r.colorScheme=t.dark?'dark':'light';for(const [k,v] of [['--accent',t.accent],['--bg',t.bg],['--bar',t.bar],['--fg',t.fg],['--muted',t.muted]])if(ok(v)&&v)r.setProperty(k,v);}catch{}})();"""


def serve(status,root=Path('/app'),address='172.30.32.2'):
    server=ThreadingHTTPServer(('0.0.0.0',8099),Handler)
    server.ingress_address=address;server.root=root;server.status=status
    return server


# The panel is a dashboard of the HA already open around this page. A nested iframe
# would boot a second HA with all its cards, and phones may not authenticate it.
MOBILE_OVERVIEW_SCRIPT=r"""(()=>{const link=document.getElementById('open-dashboard'),path=link.getAttribute('href'),launch=document.querySelector('.mobile-launch');try{const outer=window.top;if(outer!==window&&outer.document.querySelector('home-assistant')){outer.history.replaceState(outer.history.state,'',path);outer.dispatchEvent(new outer.CustomEvent('location-changed',{detail:{replace:true}}));}}catch{}setTimeout(()=>{launch.hidden=false;},1500);})();"""
