#!/usr/bin/env python3
"""Exercise the actual non-root container with a fixture upload and restart."""
import io, json, subprocess, sys, time, urllib.request, urllib.error, uuid, zipfile
image=sys.argv[1]
name='marketplace-smoke-'+uuid.uuid4().hex[:10]
token='container-smoke-token-not-a-production-secret-123'
password='container-smoke-password-not-a-production-secret'
def docker(*args): return subprocess.check_output(['docker',*args],text=True).strip()
def request(path,data=None):
    r=urllib.request.Request('http://127.0.0.1:'+port+path,data=data,headers={'Authorization':'Bearer '+token,'Content-Type':'application/octet-stream','Idempotency-Key':'smoke-upload'})
    with urllib.request.urlopen(r,timeout=30) as res:return res.status,json.load(res)
def ready():
    for _ in range(90):
        try:
            status,_=request('/health/ready')
            if status==200:return
        except Exception:pass
        time.sleep(1)
    raise RuntimeError('container did not become ready')
try:
    docker('run','-d','--name',name,'-p','127.0.0.1::8080','-e','API_TOKEN='+token,'-e','ADMIN_PASSWORD='+password,'-e','PUBLIC_URL=http://localhost',image)
    port=docker('port',name,'8080/tcp').rsplit(':',1)[1]
    ready()
    b=io.BytesIO()
    with zipfile.ZipFile(b,'w',zipfile.ZIP_DEFLATED) as z:
        z.writestr('extension/package.json',json.dumps({'publisher':'smoke','name':'hello','version':'1.0.0','engines':{'vscode':'^1.0.0'}}))
        z.writestr('extension.vsixmanifest','<PackageManifest><Metadata><Identity Publisher="smoke" Id="hello" Version="1.0.0"/></Metadata></PackageManifest>')
    status,result=request('/api/v1/extensions',b.getvalue())
    assert status==201 and result['package']['status']=='stored',result
    status,result=request('/api/v1/extensions',b.getvalue())
    assert status==200 and result['duplicate'],result
    docker('restart','--time','5',name)
    port=docker('port',name,'8080/tcp').rsplit(':',1)[1]
    ready()
    status,result=request('/api/v1/extensions')
    assert result['total']==1 and result['packages'][0]['status']=='stored',result
    print('Container smoke passed: non-root startup, durable upload, duplicate retry, restart reconciliation.')
finally:
    subprocess.run(['docker','logs','--tail','10',name],check=False)
    subprocess.run(['docker','rm','-f',name],stdout=subprocess.DEVNULL,check=False)
