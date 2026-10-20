#!/usr/bin/env bash
# Funções comuns dos hooks do Claude Code (.claude/settings.json).
# O hook recebe um JSON no stdin: {"tool_name", "tool_input": {...}, "cwd", ...}.
# Sem jq na máquina, extraímos campos com node ou python; último recurso: grep.

# posix <caminho> → barras normais e drive Windows no formato do Git Bash (/c/...)
posix() {
  local p="${1//\\//}"
  case "$p" in
    [A-Za-z]:/*) local d="${p:0:1}"; p="/${d,,}${p:2}" ;;
  esac
  printf '%s' "$p"
}

RAIZ="$(posix "${CLAUDE_PROJECT_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}")"

# campo_json <json> <caminho.pontuado>  → valor (string) ou vazio
campo_json() {
  local json="$1" caminho="$2"
  if command -v node >/dev/null 2>&1; then
    node -e '
let d="";process.stdin.on("data",c=>d+=c).on("end",()=>{
  const ks=process.argv[1].split(".");
  try{let o=JSON.parse(d);for(const k of ks)o=o?.[k];process.stdout.write(o==null?"":String(o));return}catch(e){}
  // JSON malformado (ex.: barra invertida sem escape): pega a última chave por regex
  const k=ks[ks.length-1];
  const m=d.match(new RegExp("\""+k+"\"\\s*:\\s*\"((?:[^\"\\\\]|\\\\.)*)\""));
  if(m)process.stdout.write(m[1].replace(/\\(.)/g,"$1"));
})' "$caminho" <<<"$json"
    return
  fi
  for py in python python3; do
    if command -v "$py" >/dev/null 2>&1; then
      "$py" -I -c 'import json,sys,re
d=sys.stdin.read(); ks=sys.argv[1].split(".")
try:
    o=json.loads(d)
    for k in ks:
        o=o.get(k) if isinstance(o,dict) else None
    sys.stdout.write("" if o is None else str(o)); sys.exit(0)
except Exception:
    pass
m=re.search(r"\"%s\"\s*:\s*\"((?:[^\"\\\\]|\\\\.)*)\"" % re.escape(ks[-1]), d)
if m: sys.stdout.write(re.sub(r"\\\\(.)", r"\\1", m.group(1)))' "$caminho" <<<"$json" 2>/dev/null && return
    fi
  done
  local chave="${caminho##*.}"
  printf '%s' "$json" | grep -o "\"$chave\"[[:space:]]*:[[:space:]]*\"[^\"]*\"" | head -1 | sed 's/.*:[[:space:]]*"\(.*\)"/\1/'
}

# relativo <caminho> → caminho relativo à raiz (ou o próprio, normalizado, se fora dela)
relativo() {
  local p; p="$(posix "$1")"
  case "$p" in
    "$RAIZ"/*) printf '%s' "${p#"$RAIZ"/}" ;;
    *) printf '%s' "$p" ;;
  esac
}
