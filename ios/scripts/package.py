#!/usr/bin/env python3
"""Package an unsigned device build, enforce a small bundle, retain one version."""
import argparse, hashlib, json, plistlib, shutil, zipfile
from pathlib import Path

p=argparse.ArgumentParser();p.add_argument('--app',type=Path,required=True);p.add_argument('--out',type=Path,required=True);args=p.parse_args()
app=args.app.resolve();out=args.out.resolve();out.mkdir(parents=True,exist_ok=True)
info=plistlib.loads((app/'Info.plist').read_bytes())
assert info['CFBundleIdentifier']=='com.meteor.resourcelens.iphone'
assert info['CFBundleSupportedPlatforms']==['iPhoneOS']
assert not info.get('NSAppTransportSecurity',{}).get('NSAllowsArbitraryLoads',False)
assert not (app/'embedded.mobileprovision').exists(), 'This script intentionally packages only unsigned previews'
files=[file for file in app.rglob('*') if file.is_file()]
size=sum(file.stat().st_size for file in files)
assert size<=8*1024*1024, f'iPhone bundle {size} bytes exceeds the 8 MiB budget'
assert not any(file.name.lower().startswith(('python','ffmpeg','yt-dlp','node')) for file in files)
version=info['CFBundleShortVersionString'];dest=out/f'ResourceLens-iPhone-{version}-UNSIGNED.ipa'
with zipfile.ZipFile(dest,'w',zipfile.ZIP_DEFLATED,compresslevel=9) as archive:
    for file in files: archive.write(file,'Payload/'+app.name+'/'+str(file.relative_to(app)))
digest=hashlib.sha256(dest.read_bytes()).hexdigest()
dest.with_suffix('.ipa.sha256').write_text(digest+'  '+dest.name+'\n')
metadata={'version':version,'bundleBytes':size,'ipaBytes':dest.stat().st_size,'sha256':digest,'budgetBytes':8*1024*1024,'signed':False,'requiresMacWhileUsing':False,'musicModule':False}
(out/'footprint.json').write_text(json.dumps(metadata,ensure_ascii=False,indent=2)+'\n')
for old in out.glob('ResourceLens-iPhone-*-UNSIGNED.ipa*'):
    if old not in [dest,dest.with_suffix('.ipa.sha256')]:old.unlink()
print(json.dumps(metadata))
