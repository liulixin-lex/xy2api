#!/usr/bin/env python3
"""Prove disabled P0a never enters parsing/chain/classification on real paths.

Run with the same Go environment for both trees:
 python3 tools/cache-trace/check_disabled.py --baseline <clean-baseline-tree> --output <evidence-dir>
The baseline is read only: identical test code is supplied through Go overlay.
The candidate overlay inserts panic stubs; production sources remain unchanged.
"""
import argparse
import json
from pathlib import Path
import re
import subprocess
import time

parser=argparse.ArgumentParser(description=__doc__)
parser.add_argument("--baseline",type=Path,required=True)
parser.add_argument("--output",type=Path,required=True)
args=parser.parse_args()
root=Path(__file__).resolve().parents[2]
output=args.output.resolve();output.mkdir(parents=True,exist_ok=False)
service=root/"backend/internal/service"
replacements={};instrumented=[]
for name in ("cache_prefix_chain.go","cache_prefix_integration.go","cache_trace.go","cache_trace_settlement.go"):
 path=service/name
 if not path.exists(): continue
 source=path.read_text()
 if name.startswith("cache_prefix_"):
  def stub(match):
   signature=match.group(0);instrumented.append(signature)
   return signature+'\n panic("disabled P0a entered block processing")'
  source=re.sub(r"^func [^\n]+\{",stub,source,flags=re.M)
 else:
  count=source.count("root := parseRawJSONView(body)")
  if count:
   instrumented.append(name+": P0a JSON sites="+str(count))
   source=source.replace("root := parseRawJSONView(body)",'panic("disabled P0a entered JSON observation")\n root := parseRawJSONView(body)')
  for function in ("classifyCacheTrace","emitCacheTrace"):
   pattern=r"^func [^\n]*\b"+function+r"\([^\n]+\{"
   def stub_other(match):
    instrumented.append(match.group(0))
    return match.group(0)+'\n panic("disabled P0a entered classification/emission")'
   source=re.sub(pattern,stub_other,source,flags=re.M)
 target=output/name;target.write_text(source);replacements[str(path)]=str(target)
overlay=output/"candidate-overlay.json";overlay.write_text(json.dumps({"Replace":replacements},indent=2))
results={"instrumented":instrumented,"commands":[]}
def run(label,tree,command):
 start=time.monotonic()
 with (output/(label+".log")).open("w") as log:
  rc=subprocess.run(command,cwd=tree/"backend",stdout=log,stderr=subprocess.STDOUT).returncode
 results[ label ]={"rc":rc,"elapsed_s":round(time.monotonic()-start,2),"command":command}
 (output/"summary.json").write_text(json.dumps(results,indent=2))
 if rc:raise SystemExit(label+" failed; inspect "+str(output/(label+".log")))
run("entry-stubs",root,["go","test","-tags=unit","-p=1","-overlay",str(overlay),"-count=1","-run","^TestCacheTraceDisabled(ForwardEntryStub|PipelineSuccess)$","./internal/service/"])
fixture="cache_trace_disabled_fixture_test.go"
base_overlay=output/"baseline-overlay.json"
base_overlay.write_text(json.dumps({"Replace":{str(args.baseline.resolve()/"backend/internal/service"/fixture):str(service/fixture)}}))
for label,tree,extra in [("baseline",args.baseline.resolve(),["-overlay",str(base_overlay)]),("candidate",root,[])]:
 run(label,tree,["go","test","-tags=unit","-p=1",*extra,"-run","^TestCacheTraceDisabledHasNoReadsOrAllocations$","-bench","^BenchmarkCacheTraceDisabledPipeline$","-benchmem","-benchtime=5000x","-count=3","./internal/service/"])
 stats={};pending=None
 for line in (output/(label+".log")).read_text().splitlines():
  name=re.search(r"(BenchmarkCacheTraceDisabledPipeline/\S+)-\d+",line)
  if name:pending=name[1]
  metric=re.search(r"\d+\s+[\d.]+ ns/op\s+(\d+) B/op\s+(\d+) allocs/op",line)
  # Existing billing logs can appear between the benchmark name and result.
  if pending and metric:
   stats.setdefault(pending,[]).append({"bytes":int(metric[1]),"allocs":int(metric[2])})
   pending=None
 if len(stats)!=4:raise SystemExit("missing benchmark cases: "+label)
 results[label]["allocations"]=stats
comparison={}
for name,rows in results["candidate"]["allocations"].items():
 base=results["baseline"]["allocations"][name]
 comparison[name]={metric:min(r[metric] for r in rows)-min(r[metric] for r in base) for metric in ("bytes","allocs")}
results["delta"]=comparison
# Full pipelines include pre-existing goroutines and pools: B/op varies with
# scheduling even for identical builds. Gate allocation counts; preserve all
# byte samples, without claiming byte-for-byte heap equivalence. The separate
# entry-guard test asserts exactly zero allocations attributable to P0a.
results["pass"]=all(v["allocs"]<=0 for v in comparison.values())
results["byte_comparison_note"]="B/op samples are descriptive; background goroutines and pool reuse vary. P0a entry guards are independently required to allocate zero."
(output/"summary.json").write_text(json.dumps(results,indent=2))
print(json.dumps(results,indent=2))
raise SystemExit(0 if results["pass"] else 1)
