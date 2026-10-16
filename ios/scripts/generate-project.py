#!/usr/bin/env python3
"""Generate the committed Xcode project using only the Python standard library."""
from pathlib import Path
import hashlib, json
root=Path(__file__).resolve().parents[1]
def ident(value): return hashlib.sha1(value.encode()).hexdigest()[:24].upper()
def q(value): return json.dumps(value)
app_sources=sorted(str(p.relative_to(root)) for p in (root/'ResourceLens').glob('*.swift'))+sorted(str(p.relative_to(root)) for p in (root/'Sources/ResourceLensCore').glob('*.swift'))
test_sources=['ResourceLensUITests/ResourceLensUITests.swift']
files=app_sources+test_sources+['Resources/Assets.xcassets','Resources/Info.plist','Resources/Info-Debug.plist']
lines=['// !$*UTF8*$!','{ archiveVersion = 1; classes = {}; objectVersion = 56; objects = {']
for file in files:
 kind='sourcecode.swift' if file.endswith('.swift') else 'folder.assetcatalog' if file.endswith('.xcassets') else 'text.plist.xml'
 lines.append(f'{ident(file)} = {{isa = PBXFileReference; lastKnownFileType = {kind}; path = {q(file)}; sourceTree = "<group>"; }};')
 for target in ['app' if file in app_sources or file.endswith('xcassets') else 'ui']:
  if file.endswith('.swift') or file.endswith('xcassets'):lines.append(f'{ident(target+file)} = {{isa = PBXBuildFile; fileRef = {ident(file)}; }};')
for target,product,kind in [('app','ResourceLensPhone.app','wrapper.application'),('ui','ResourceLensUITests.xctest','wrapper.cfbundle')]:
 lines.append(f'{ident(target+"product")} = {{isa = PBXFileReference; explicitFileType = {kind}; includeInIndex = 0; path = {q(product)}; sourceTree = BUILT_PRODUCTS_DIR; }};')
lines.append(f'{ident("mainGroup")} = {{isa = PBXGroup; children = ({",".join(ident(f) for f in files)},{ident("products")}); sourceTree = "<group>"; }};')
lines.append(f'{ident("products")} = {{isa = PBXGroup; children = ({ident("appproduct")},{ident("uiproduct")}); name = Products; sourceTree = "<group>"; }};')
lines.append(f'{ident("proxy")} = {{isa = PBXContainerItemProxy; containerPortal = {ident("project")}; proxyType = 1; remoteGlobalIDString = {ident("app")}; remoteInfo = ResourceLensPhone; }};')
lines.append(f'{ident("dependency")} = {{isa = PBXTargetDependency; target = {ident("app")}; targetProxy = {ident("proxy")}; }};')
for target,sources in [('app',app_sources),('ui',test_sources)]:
 for phase,isa,entries in [('sources','PBXSourcesBuildPhase',[ident(target+f) for f in sources]),('resources','PBXResourcesBuildPhase',[ident('appResources/Assets.xcassets')] if target=='app' else []),('frameworks','PBXFrameworksBuildPhase',[])]:
  lines.append(f'{ident(target+phase)} = {{isa = {isa}; buildActionMask = 2147483647; files = ({",".join(entries)}); runOnlyForDeploymentPostprocessing = 0; }};')
 name='ResourceLensPhone' if target=='app' else 'ResourceLensUITests'
 product_type='com.apple.product-type.application' if target=='app' else 'com.apple.product-type.bundle.ui-testing'
 lines.append(f'{ident(target)} = {{isa = PBXNativeTarget; buildConfigurationList = {ident(target+"configs")}; buildPhases = ({ident(target+"sources")},{ident(target+"frameworks")},{ident(target+"resources")}); buildRules = (); dependencies = ({ident("dependency") if target=="ui" else ""}); name = {name}; productName = {name}; productReference = {ident(target+"product")}; productType = {q(product_type)}; }};')
for target in ['project','app','ui']:
 configs=[]
 for config in ['Debug','Release']:
  key=ident(target+config);configs.append(key)
  settings={'SDKROOT':'iphoneos','IPHONEOS_DEPLOYMENT_TARGET':'17.0','SWIFT_VERSION':'5.0','CLANG_ENABLE_MODULES':'YES','CODE_SIGN_STYLE':'Automatic','TARGETED_DEVICE_FAMILY':'1,2','SUPPORTED_PLATFORMS':'iphoneos iphonesimulator macosx','SUPPORTS_MACCATALYST':'YES'}
  if target=='app':settings.update({'PRODUCT_NAME':'ResourceLensPhone','PRODUCT_BUNDLE_IDENTIFIER':'com.meteor.resourcelens.iphone','INFOPLIST_FILE':'Resources/Info-Debug.plist' if config=='Debug' else 'Resources/Info.plist','ASSETCATALOG_COMPILER_APPICON_NAME':'AppIcon','CODE_SIGN_ENTITLEMENTS[sdk=macosx*]':'Resources/Catalyst.entitlements','MARKETING_VERSION':'0.1.0','CURRENT_PROJECT_VERSION':'1','ENABLE_PREVIEWS':'YES','SWIFT_EMIT_LOC_STRINGS':'NO','LD_RUNPATH_SEARCH_PATHS':'$(inherited) @executable_path/Frameworks'})
  if target=='ui':settings.update({'PRODUCT_NAME':'ResourceLensUITests','PRODUCT_BUNDLE_IDENTIFIER':'com.meteor.resourcelens.iphone.uitests','GENERATE_INFOPLIST_FILE':'YES','TEST_TARGET_NAME':'ResourceLensPhone'})
  if config=='Debug':settings.update({'SWIFT_ACTIVE_COMPILATION_CONDITIONS':'DEBUG','SWIFT_OPTIMIZATION_LEVEL':'-Onone','ONLY_ACTIVE_ARCH':'YES'})
  else:settings.update({'SWIFT_OPTIMIZATION_LEVEL':'-O','SWIFT_COMPILATION_MODE':'wholemodule','DEBUG_INFORMATION_FORMAT':'dwarf-with-dsym','STRIP_INSTALLED_PRODUCT':'YES','DEAD_CODE_STRIPPING':'YES'})
  lines.append(f'{key} = {{isa = XCBuildConfiguration; name = {config}; buildSettings = {{'+''.join(f'{q(k)} = {q(v)};' for k,v in settings.items())+'}; };')
 lines.append(f'{ident(target+"configs")} = {{isa = XCConfigurationList; buildConfigurations = ({",".join(configs)}); defaultConfigurationIsVisible = 0; defaultConfigurationName = Release; }};')
lines.append(f'{ident("project")} = {{isa = PBXProject; attributes = {{BuildIndependentTargetsInParallel = YES; LastUpgradeCheck = 1600; TargetAttributes = {{{ident("ui")} = {{TestTargetID = {ident("app")}; }}; }}; }}; buildConfigurationList = {ident("projectconfigs")}; compatibilityVersion = "Xcode 14.0"; developmentRegion = "zh-Hans"; hasScannedForEncodings = 0; knownRegions = (en,"zh-Hans",Base); mainGroup = {ident("mainGroup")}; productRefGroup = {ident("products")}; projectDirPath = ""; projectRoot = ""; targets = ({ident("app")},{ident("ui")}); }};')
lines.append('}; rootObject = '+ident('project')+'; }')
(root/'ResourceLens.xcodeproj/project.pbxproj').write_text('\n'.join(lines)+'\n')
scheme=f'''<?xml version="1.0" encoding="UTF-8"?>
<Scheme LastUpgradeVersion="1600" version="1.3"><BuildAction parallelizeBuildables="YES" buildImplicitDependencies="YES"><BuildActionEntries><BuildActionEntry buildForTesting="YES" buildForRunning="YES" buildForProfiling="YES" buildForArchiving="YES" buildForAnalyzing="YES"><BuildableReference BuildableIdentifier="primary" BlueprintIdentifier="{ident('app')}" BuildableName="ResourceLensPhone.app" BlueprintName="ResourceLensPhone" ReferencedContainer="container:ResourceLens.xcodeproj"/></BuildActionEntry></BuildActionEntries></BuildAction><TestAction buildConfiguration="Debug" selectedDebuggerIdentifier="Xcode.DebuggerFoundation.Debugger.LLDB" selectedLauncherIdentifier="Xcode.IDEFoundation.Launcher.LLDB" shouldUseLaunchSchemeArgsEnv="YES"><Testables><TestableReference skipped="NO"><BuildableReference BuildableIdentifier="primary" BlueprintIdentifier="{ident('ui')}" BuildableName="ResourceLensUITests.xctest" BlueprintName="ResourceLensUITests" ReferencedContainer="container:ResourceLens.xcodeproj"/></TestableReference></Testables></TestAction><LaunchAction buildConfiguration="Debug" selectedDebuggerIdentifier="Xcode.DebuggerFoundation.Debugger.LLDB" selectedLauncherIdentifier="Xcode.IDEFoundation.Launcher.LLDB" launchStyle="0" useCustomWorkingDirectory="NO" ignoresPersistentStateOnLaunch="NO" debugDocumentVersioning="YES" allowLocationSimulation="YES"><BuildableProductRunnable runnableDebuggingMode="0"><BuildableReference BuildableIdentifier="primary" BlueprintIdentifier="{ident('app')}" BuildableName="ResourceLensPhone.app" BlueprintName="ResourceLensPhone" ReferencedContainer="container:ResourceLens.xcodeproj"/></BuildableProductRunnable></LaunchAction><ProfileAction buildConfiguration="Release" shouldUseLaunchSchemeArgsEnv="YES" savedToolIdentifier="" useCustomWorkingDirectory="NO" debugDocumentVersioning="YES"/><AnalyzeAction buildConfiguration="Debug"/><ArchiveAction buildConfiguration="Release" revealArchiveInOrganizer="YES"/></Scheme>'''
(root/'ResourceLens.xcodeproj/xcshareddata/xcschemes/ResourceLensPhone.xcscheme').write_text(scheme+'\n')
