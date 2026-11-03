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
        return re.sub(r'`([^`]+)`',r'<code>\1</code>',value)
    lines=[]; paragraph=[]; code=False; listing=False; headings=0
    def flush():
        if paragraph:
            lines.append('<p>'+inline(' '.join(paragraph))+'</p>');paragraph.clear()
    for line in text.splitlines():
        if line.startswith('```'):
            flush()
            if listing:lines.append('</ul>');listing=False
            lines.append('</code></pre>' if code else '<pre><code>');code=not code
        elif code:lines.append(html.escape(line)+'\n')
        else:
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
    flush()
    if listing:lines.append('</ul>')
    if code:lines.append('</code></pre>')
    return '\n'.join(lines)


WIKI_PAGES=[
    ('Начало работы', [('Home','Обзор','Всё для первого запуска'),('Installation','Установка приложения','Установите и настройте приложение в HA'),('Compatibility','Совместимость','Модели, прошивки и требования'),('Bridge','Подключение колонки','SSH, ключ и установка моста')]),
    ('Каждый день', [('Commands','Голосовые команды','Фразы, автоматизации и устройства'),('LLM','Умные ответы','LLM и маршрутизация запросов')]),
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
        content='<div class="docs-hero"><span class="docs-eyebrow">СПРАВОЧНИК АЛИСЫ</span><h1>От первой колонки<br>до привычных команд.</h1><p>Подключите станцию, настройте фразы и управляйте домом голосом. Инструкции всегда рядом — даже без интернета.</p></div><div class="docs-tasks">'+cards+'</div><section class="docs-callout"><span aria-hidden="true">i</span><div><h2>Перед подключением</h2><p>Нужны Home Assistant OS с Supervisor и совместимая колонка с уже настроенным root SSH. Приложение устанавливает мост; способ подготовки колонки зависит от модели и прошивки.</p><a href="wiki/Compatibility">Проверить совместимость →</a></div></section><div class="docs-home-links"><a href="wiki/Recovery">Что-то не работает?<span>Обновление и восстановление →</span></a><a href="wiki/Migration">Уже пользуетесь мостом?<span>Переход со старого моста →</span></a></div>'
    else:
        content='<div class="docs-breadcrumb"><a href="wiki/Home">Справочник</a><span>/</span>'+html.escape(meta[1])+'</div><article class="docs-article">'+markdown(source)+'</article>'
        position=next((i for i,p in enumerate(pages) if p[0]==name),0)
        pager=''
        for i,label in ((position-1,'← Предыдущая'),(position+1,'Следующая →')):
            if 0<=i<len(pages):pager+='<a href="wiki/'+pages[i][0]+'"><small>'+label+'</small><strong>'+html.escape(pages[i][1])+'</strong></a>'
        content+='<nav class="docs-pager" aria-label="Страницы справочника">'+pager+'</nav>'
    aside='<aside class="docs-toc"><span class="docs-eyebrow">НА ЭТОЙ СТРАНИЦЕ</span>'+''.join(toc)+'</aside>' if toc and name!='Home' else ''
    return '<style>'+WIKI_STYLE+'</style><div class="docs-layout"><aside class="docs-sidebar"><div class="docs-sidebar-title">Справочник<span>АЛИСА × HOME ASSISTANT</span></div><label class="docs-search"><span aria-hidden="true">⌕</span><input id="docs-search" type="search" placeholder="Найти в справочнике" aria-label="Поиск по всем статьям" autocomplete="off"></label><div id="docs-search-results" hidden aria-live="polite"></div><details class="docs-mobile-nav"><summary>Разделы справочника</summary>'+nav+'</details><div class="docs-desktop-nav">'+nav+'</div><p class="docs-offline"><span>●</span> Доступно без интернета</p></aside><div class="docs-content">'+content+'</div>'+aside+'</div><script type="application/json" id="docs-index">'+search+'</script><script>'+WIKI_SCRIPT+'</script>'


WIKI_SCRIPT=r"""(()=>{const input=document.getElementById('docs-search'),box=document.getElementById('docs-search-results');if(!input||!box)return;const pages=JSON.parse(document.getElementById('docs-index').textContent);input.addEventListener('input',()=>{const query=input.value.trim().toLocaleLowerCase();box.replaceChildren();box.hidden=!query;if(!query)return;const results=pages.filter(p=>(p.title+' '+p.text).toLocaleLowerCase().includes(query)).slice(0,10);if(!results.length){const empty=document.createElement('p');empty.textContent='Ничего не найдено. Попробуйте другое слово.';box.append(empty);}for(const p of results){const link=document.createElement('a'),title=document.createElement('strong'),desc=document.createElement('span');link.href='wiki/'+p.slug;title.textContent=p.title;desc.textContent=p.description;link.append(title,desc);box.append(link);}});})();"""

WIKI_STYLE=r"""
.docs-layout{--docs-muted:#98a3b8;--docs-line:#ffffff12;--docs-accent:#b6a4ff;display:grid;grid-template-columns:236px minmax(0,800px) 190px;gap:48px;max-width:1420px;margin:0 auto;padding:40px 32px 80px;color:#ecf0f8;font:15px/1.65 system-ui,sans-serif}.docs-layout *{box-sizing:border-box}.docs-layout a{color:inherit;text-decoration:none}.docs-layout a:focus-visible,.docs-layout input:focus-visible,.docs-layout summary:focus-visible{outline:2px solid var(--docs-accent);outline-offset:4px}.docs-sidebar{min-width:0;position:sticky;top:28px;align-self:start}.docs-sidebar-title{font-size:23px;font-weight:750;letter-spacing:-.5px;margin-bottom:24px}.docs-sidebar-title span{display:block;color:var(--docs-muted);font-size:9px;letter-spacing:1.8px;margin-top:5px}.docs-search{display:flex;gap:10px;align-items:center;background:#ffffff06;border:1px solid #ffffff18;border-radius:11px;padding:10px 12px}.docs-search>span{font-size:22px;line-height:1;color:var(--docs-muted)}.docs-search input{color:inherit;background:none;border:0;min-width:0;width:100%;font:inherit;font-size:12px;padding:2px;outline:0}.docs-nav-group{margin:25px 0}.docs-nav-group h2{font-size:10px!important;text-transform:uppercase;letter-spacing:1.4px;color:var(--docs-muted);margin:0 12px 10px!important;line-height:1.3}.docs-nav-group a{display:block;padding:8px 12px;border-radius:8px;font-size:13px;color:#aeb8ca;line-height:1.4;margin:3px 0}.docs-nav-group a:hover{background:#ffffff06;color:#fff}.docs-nav-group a[aria-current]{background:#b6a4ff12;color:#c4ceff;font-weight:650;box-shadow:inset 2px 0 #b6a4ff}.docs-offline{margin:30px 12px 0;color:var(--docs-muted);font-size:11px}.docs-offline span{color:#77cdb7;font-size:8px;margin-right:6px}.docs-content{min-width:0;padding-top:8px}.docs-eyebrow{font-size:10px;font-weight:700;letter-spacing:1.9px;color:#9aafff}.docs-hero h1{font-size:clamp(30px,3.2vw,46px);font-weight:750;letter-spacing:-1.8px;line-height:1.15;margin:18px 0 20px}.docs-hero>p{font-size:16px;color:#a8b3c6;max-width:570px;line-height:1.75;margin-bottom:30px}.docs-tasks{display:grid;grid-template-columns:1fr 1fr;gap:14px;margin-top:32px}.docs-task{position:relative;padding:24px!important;border:1px solid #ffffff13;border-radius:16px;background:linear-gradient(140deg,#1c2230,#151922);transition:border-color .2s,transform .2s}.docs-task:hover{border-color:#b6a4ff66;transform:translateY(-2px)}.docs-task-number{color:#b6a4ff;font-size:11px;letter-spacing:1px}.docs-task h2{font-size:17px;letter-spacing:-.3px;margin:18px 0 7px;line-height:1.3}.docs-task p{color:var(--docs-muted);font-size:13px;line-height:1.6;margin:0;max-width:230px}.docs-task-arrow{position:absolute;right:22px;top:20px;color:#8c9bb8;font-size:20px}.docs-callout{display:flex;gap:16px;margin-top:24px;padding:22px;border:1px solid #92b2ff24;border-radius:14px;background:#7597ed08}.docs-callout>span{border:1px solid #92b2ff66;color:#b6a4ff;border-radius:50%;width:20px;height:20px;text-align:center;font-size:12px;flex-shrink:0;margin-top:2px}.docs-callout h2{font-size:14px;margin:0 0 8px}.docs-callout p{font-size:13px;color:#a8b3c6;margin:0 0 10px}.docs-callout a{color:#b6a4ff;font-size:12px}.docs-home-links{display:grid;grid-template-columns:1fr 1fr;gap:24px;margin-top:30px}.docs-home-links>a{font-size:13px;color:#c6cfdd}.docs-home-links span{display:block;font-size:11px;color:var(--docs-muted);margin-top:6px}.docs-breadcrumb{display:flex;gap:12px;font-size:12px;color:var(--docs-muted);margin-bottom:24px;flex-wrap:wrap}.docs-breadcrumb a:hover{color:#b6a4ff}.docs-breadcrumb>span{color:#4b5569}.docs-article{font-size:15px;line-height:1.85}.docs-article h1{font-size:36px;line-height:1.2;letter-spacing:-1.2px;margin:0 0 28px;max-width:680px}.docs-article h2{font-size:22px;letter-spacing:-.5px;line-height:1.4;margin:42px 0 16px;padding-top:12px;border-top:1px solid var(--docs-line)}.docs-article h3{font-size:17px;line-height:1.5;margin-top:28px}.docs-article h1,.docs-article h2,.docs-article h3{scroll-margin-top:28px}.docs-article p{color:#bdc6d6;margin:0 0 18px}.docs-article ul{padding-left:22px;color:#bdc6d6;margin:15px 0 22px}.docs-article li{padding-left:5px;margin:9px 0}.docs-article a{color:#b6a4ff;text-decoration:underline;text-underline-offset:3px}.docs-article code{font-size:.86em;color:#c6d6ff;background:#ffffff07;border:1px solid #ffffff0c;border-radius:5px;padding:2px 5px;overflow-wrap:anywhere}.docs-article pre{background:#0a0e16;border:1px solid #ffffff12;padding:20px;border-radius:12px;overflow:auto;line-height:1.65;margin:22px 0}.docs-article pre code{border:0;background:none;padding:0;white-space:pre;overflow-wrap:normal}.docs-toc{position:sticky;top:32px;align-self:start;border-left:1px solid var(--docs-line);padding:8px 0 8px 20px}.docs-toc>.docs-eyebrow{font-size:9px;color:#7f8ca4;letter-spacing:1.3px;display:block;margin-bottom:15px}.docs-toc a{display:block;font-size:11px;color:var(--docs-muted);margin:12px 0;line-height:1.6}.docs-toc a:hover{color:#b6a4ff}.docs-pager{display:grid!important;grid-template-columns:1fr 1fr;gap:15px;margin-top:42px;padding:22px 0!important;border-top:1px solid var(--docs-line)}.docs-pager a{padding:16px;border:1px solid var(--docs-line);border-radius:12px;display:flex;flex-direction:column}.docs-pager a:hover{border-color:#b6a4ff55}.docs-pager small{font-size:10px;color:var(--docs-muted)}.docs-pager strong{font-size:13px;font-weight:550;margin-top:6px}.docs-mobile-nav{display:none}#docs-search-results{margin-top:8px;background:#1b2130;border:1px solid #ffffff18;border-radius:12px;max-height:420px;overflow:auto;padding:6px}#docs-search-results[hidden]{display:none}#docs-search-results a{display:block;padding:10px;border-radius:7px}#docs-search-results a:hover{background:#ffffff08}#docs-search-results strong{display:block;font-size:12px}#docs-search-results span,#docs-search-results p{font-size:11px;color:var(--docs-muted);line-height:1.5}#docs-search-results span{display:block;margin-top:4px}
@media(min-width:1500px){.docs-layout{gap:58px}}@media(max-width:1200px){.docs-layout{grid-template-columns:218px minmax(0,1fr);gap:36px}.docs-toc{display:none}}@media(max-width:760px){.docs-layout{display:block;padding:24px 20px 50px}.docs-sidebar{position:static}.docs-sidebar-title{font-size:20px;margin-bottom:16px}.docs-sidebar-title span{display:none}.docs-search{max-width:none}.docs-mobile-nav{display:block;border-bottom:1px solid var(--docs-line);margin:12px 0 26px}.docs-mobile-nav>summary{font-size:12px;color:#b6a4ff;padding:12px 0;cursor:pointer}.docs-desktop-nav,.docs-offline{display:none}.docs-content{padding:0}.docs-hero h1{font-size:34px;letter-spacing:-1px}.docs-hero>p{font-size:14px}.docs-task{padding:19px!important}.docs-task h2{font-size:15px}.docs-task p{font-size:12px}.docs-article h1{font-size:29px}.docs-article{font-size:14px}.docs-home-links{gap:18px}.docs-breadcrumb{font-size:11px}}@media(max-width:420px){.docs-tasks{grid-template-columns:1fr}.docs-task h2{margin-top:12px}.docs-home-links{grid-template-columns:1fr}.docs-pager{grid-template-columns:1fr}}
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
        overview='<iframe' in body
        def tab(href,label,active):
            return '<a href="'+href+'"'+(' aria-current="page"' if active else '')+'>'+label+'</a>'
        navigation=tab('./','Обзор',overview)+tab('wiki/Home','Руководство',not overview)
        skip=html.escape((urlsplit(getattr(self,'path','/')).path.lstrip('/') or './')+'#content',quote=True)
        return ('<!doctype html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><base href="'+html.escape(base,quote=True)+'"><title>Алиса — '+('Обзор' if overview else 'Руководство')+'</title><style>'+STYLE+'</style></head><body class="'+('app-overview' if overview else 'app-docs')+'"><a class="skip-link" href="'+skip+'">К содержимому</a><header class="app-header"><a class="app-brand" href="./" aria-label="Алиса — главная"><span class="app-mark" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M4 10v4m4-8v12m4-15v18m4-15v12m4-8v4"/></svg></span><strong>Алиса</strong><span class="app-brand-caption">HOME ASSISTANT</span></a><nav class="app-tabs" aria-label="Приложение">'+navigation+'</nav><a class="app-help" href="wiki/Installation">С чего начать <span aria-hidden="true">↗</span></a></header><main id="content" tabindex="-1">'+body+'</main></body></html>').encode()

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
        body=notice+'<iframe title="Колонки и голосовые команды" src="/'+dashboard+'/speakers"></iframe>'
        return self.shell(body,prefix.rstrip('/')+'/')


STYLE='''*{box-sizing:border-box}html{color-scheme:dark;font-family:system-ui,-apple-system,"Segoe UI",sans-serif;background:#101218;color:#edf0f7}body{margin:0}a{color:#c6b8ff}a:focus-visible,button:focus-visible{outline:2px solid #b6a4ff;outline-offset:4px}.skip-link{position:absolute;top:-60px;left:12px;padding:12px;background:#272135;z-index:10}.skip-link:focus{top:8px}.app-header{height:72px;display:flex;align-items:center;gap:40px;padding:0 max(28px,calc((100vw - 1420px)/2));border-bottom:1px solid #ffffff0e;background:#12151c}.app-brand{display:flex;align-items:center;gap:12px;color:#f1effa;text-decoration:none;flex-shrink:0}.app-brand strong{font-size:20px;letter-spacing:-.5px}.app-mark{display:grid;place-items:center;width:34px;height:34px;border-radius:11px;background:#b6a4ff16;color:#bdadff}.app-mark svg{width:22px;height:22px}.app-brand-caption{font-size:8px;letter-spacing:1.5px;color:#8993a8;padding-left:4px}.app-tabs{display:flex;gap:8px;height:100%;align-items:center}.app-tabs a{display:flex;align-items:center;height:38px;padding:0 18px;text-decoration:none;font-size:13px;font-weight:600;color:#9ea9bc;border-radius:10px}.app-tabs a:hover{background:#ffffff06;color:#ece8ff}.app-tabs a[aria-current]{background:#b6a4ff14;color:#c7baff}.app-help{margin-left:auto;text-decoration:none;font-size:12px;color:#9ea9bc;white-space:nowrap}.app-help span{margin-left:8px;color:#b6a4ff}main{outline:none}.notice{max-width:1320px;margin:24px auto;padding:20px 24px;background:#b6a4ff09;border:1px solid #b6a4ff26;border-radius:14px;font-size:14px;line-height:1.6}.notice p{color:#a8b3c6;margin:8px 0}.notice a{text-decoration:none}iframe{display:block;width:100%;height:calc(100dvh - 72px);border:0;background:#101218}.app-overview{overflow:hidden}.app-overview:has(.notice){overflow:auto}.app-overview:has(.notice) iframe{height:calc(100dvh - 240px);min-height:420px}@media(max-width:760px){.app-header{height:64px;padding:0 20px;gap:20px}.app-brand-caption,.app-help{display:none}.app-tabs{margin-left:auto;gap:0}.app-tabs a{padding:0 12px;font-size:12px}.app-brand{gap:9px}.app-brand strong{font-size:18px}.app-mark{width:30px;height:30px}iframe{height:calc(100dvh - 64px)}.notice{margin:16px}}@media(prefers-reduced-motion:reduce){*,*::before,*::after{scroll-behavior:auto!important;transition:none!important}}'''


def serve(status,root=Path('/app'),address='172.30.32.2'):
    server=ThreadingHTTPServer(('0.0.0.0',8099),Handler)
    server.ingress_address=address;server.root=root;server.status=status
    return server
