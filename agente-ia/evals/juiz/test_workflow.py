"""O workflow `evals-noturnos.yml` é válido e só chama comandos que existem (sem rede)."""

from __future__ import annotations

import importlib
import re
import shlex
import shutil
import subprocess
from pathlib import Path
from typing import Any

import pytest
import yaml

pytestmark = pytest.mark.evals_pr

RAIZ_REPO = Path(__file__).resolve().parents[3]
WORKFLOW = RAIZ_REPO / ".github" / "workflows" / "evals-noturnos.yml"
PYPROJECT = RAIZ_REPO / "agente-ia" / "pyproject.toml"


@pytest.fixture(scope="module")
def wf() -> dict[str, Any]:
    dado = yaml.safe_load(WORKFLOW.read_text(encoding="utf-8"))
    assert isinstance(dado, dict)
    return dado


def _passos(wf: dict[str, Any]) -> list[dict[str, Any]]:
    return [p for job in wf["jobs"].values() for p in job["steps"]]


def _comandos_python(wf: dict[str, Any]) -> list[list[str]]:
    """Cada `python -m <módulo> args...` dos blocos `run`, com as continuações `\\` juntadas."""
    comandos: list[list[str]] = []
    for passo in _passos(wf):
        texto = re.sub(r"\\\n\s*", " ", passo.get("run", ""))
        for linha in texto.splitlines():
            if "python -m " in linha and not linha.lstrip().startswith("#"):
                comandos.append(shlex.split(linha[linha.index("python -m ") :].split(";")[0]))
    return comandos


def test_agendamento_e_disparo_manual(wf: dict[str, Any]):
    bruto: dict[Any, Any] = wf  # PyYAML 1.1 lê a chave `on` como True
    gatilhos: dict[str, Any] = bruto["on"] if "on" in bruto else bruto[True]
    assert gatilhos["schedule"][0]["cron"].count(" ") == 4
    assert "workflow_dispatch" in gatilhos
    assert gatilhos["workflow_dispatch"]["inputs"]["juiz_model"]["required"] is False


def test_permissoes_minimas_e_comentario_em_pr(wf: dict[str, Any]):
    assert wf["permissions"] == {"contents": "read", "pull-requests": "write"}
    comentario = next(p for p in _passos(wf) if "gh pr comment" in p.get("run", ""))
    assert comentario["if"] == "${{ !cancelled() && steps.regressao.outputs.regressao == 'true' }}"
    assert comentario["env"]["GH_TOKEN"] == "${{ github.token }}"
    assert "state merged" in comentario["run"] and "mergedAt" in comentario["run"]


def test_secrets_so_pelo_env_e_sem_eles_o_job_nao_falha(wf: dict[str, Any]):
    usados: set[str] = set()
    for passo in _passos(wf):
        assert "secrets." not in passo.get("run", ""), "secret interpolado no script"
        for valor in (passo.get("env") or {}).values():
            usados |= set(re.findall(r"secrets\.(\w+)", str(valor)))
    assert usados == {"TYPESAFE_API_KEY", "OPENROUTER_API_KEY"}
    conferir = next(p for p in _passos(wf) if p.get("id") == "chaves")
    assert "::warning" in conferir["run"] and "exit 1" not in conferir["run"]
    # todo passo que usa um secret só roda se o secret existe
    for passo in _passos(wf):
        if "secrets." in str(passo.get("env", "")) and passo.get("id") != "chaves":
            assert "steps.chaves.outputs." in passo["if"], passo["name"]


def test_chama_a_classificacao_jev_o_fallback_llm_e_o_juiz(wf: dict[str, Any]):
    texto = "\n".join(p.get("run", "") for p in _passos(wf))
    assert "pytest -m jev" in texto and "pytest -m llm_real" in texto
    assert "python -m evals.classificacao --fonte sintetico --jev" in texto
    assert "python -m evals.classificacao --fonte sintetico --llm" in texto
    assert "python -m evals.juiz --relatorio" in texto
    assert "python -m evals.juiz.regressao" in texto


def test_todo_python_dash_m_resolve_e_aceita_os_argumentos(wf: dict[str, Any]):
    comandos = _comandos_python(wf)
    assert len(comandos) >= 5
    for argv in comandos:
        modulo = argv[2]
        if modulo == "pytest":
            continue
        principal = importlib.import_module(
            modulo if modulo.endswith(".regressao") else f"{modulo}.__main__"
        )
        parser = principal._parser()
        parser.parse_args(argv[3:])  # SystemExit se a flag não existe


def test_marcadores_do_pytest_existem_no_pyproject(wf: dict[str, Any]):
    registrados = PYPROJECT.read_text(encoding="utf-8")
    usados = {m for p in _passos(wf) for m in re.findall(r"pytest -m (\w+)", p.get("run", ""))}
    assert usados == {"jev", "llm_real"}
    for marcador in usados:
        assert re.search(rf'"{marcador}:', registrados), marcador


def test_artefato_e_relatorios_batem_com_o_diretorio_dos_runners(wf: dict[str, Any]):
    upload = next(
        p for p in _passos(wf) if str(p.get("uses", "")).startswith("actions/upload-artifact")
    )
    assert upload["with"]["name"] == "relatorios-evals"
    assert upload["with"]["path"] == "agente-ia/evals/relatorios/"
    assert "!cancelled()" in upload["if"]
    assert next(iter(wf["jobs"].values()))["defaults"]["run"]["working-directory"] == "agente-ia"
    gitignore = (RAIZ_REPO / ".gitignore").read_text(encoding="utf-8")
    assert "agente-ia/evals/relatorios/" in gitignore


def test_actionlint_se_existir():
    exe = shutil.which("actionlint")
    if exe is None:
        pytest.skip("actionlint não instalado neste ambiente")
    r = subprocess.run([exe, str(WORKFLOW)], capture_output=True, text=True, check=False)  # noqa: S603
    assert r.returncode == 0, r.stdout + r.stderr


def test_exit_1_so_vira_aviso_se_o_relatorio_json_existe(wf: dict[str, Any]):
    """Exceção não tratada também sai com 1: sem o .json (gravado antes de comparar) o job falha."""
    for passo in _passos(wf):
        run = passo.get("run", "")
        if "python -m evals.classificacao" in run or "python -m evals.juiz --relatorio" in run:
            assert re.search(r'"\$rc" -eq 1 \] && \[ -f evals/relatorios/[\w-]+\.json \]', run), (
                passo["name"]
            )
            assert 'elif [ "$rc" -ne 0 ]; then exit "$rc"' in run, passo["name"]
        if (
            "pytest -m" in run
        ):  # teste vermelho = critério não atingido ou serviço fora: o job fica vermelho
            assert 'if [ "$rc" -ne 0 ]; then' in run and "-eq 1" not in run, passo["name"]


def test_passo_que_falha_nao_pula_os_seguintes(wf: dict[str, Any]):
    """Sem `!cancelled()`, o JEV fora do ar (passo 1) deixaria o fallback LLM e o juiz sem rodar."""
    depois_do_primeiro = [
        p
        for p in _passos(wf)[
            _passos(wf).index(
                next(p for p in _passos(wf) if "Classificação com JEV" in p.get("name", ""))
            )
            + 1 :
        ]
        if "steps.chaves.outputs" in str(p.get("if", ""))
    ]
    assert len(depois_do_primeiro) >= 6
    for passo in depois_do_primeiro:
        assert "!cancelled()" in passo["if"], passo.get("name")
