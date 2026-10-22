# mypy: disallow-untyped-defs, disallow-incomplete-defs, check-untyped-defs, disallow-any-generics, warn-return-any, strict-equality
"""CLI do juiz: `python -m evals.juiz [--relatorio caminho.md] [--ids a,b] [--max N]`.

Roda cada cenário de `evals/conversa/juiz/*.yaml` contra o grafo real com o LLM REAL
(OpenRouter) e pede a um LLM juiz a nota por critério (persona, exatidão, não inventa,
pede confirmação). Precisa de `OPENROUTER_API_KEY`; sem ela sai com código 2 e diz
isso (o workflow noturno nem chama o comando sem o secret). `--sem-executar` só carrega
e valida os cenários (sem chave, sem rede).

Modelos: o da Sofia é o `OPENROUTER_MODEL` do cérebro; o do juiz é `--modelo-juiz`, ou
`JUIZ_MODEL`, ou — se nenhum — o mesmo da Sofia (um modelo julgando a si mesmo tende a
ser leniente: configure outro).

Saídas: `.md` (default `evals/relatorios/<data>-juiz.md`) e um `.json` ao lado com
`{"chave": "juiz/noturno", "metricas": {...}}`, que o `evals.juiz.regressao` compara
com `evals/juiz/baseline.json`. Código de saída: 0 ok; 1 regressão contra o baseline;
2 cenário inválido, chave ausente ou argumento errado.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from collections.abc import Callable, Sequence
from datetime import UTC, datetime
from pathlib import Path
from typing import Any

from evals.formatos import ErroFormato, carregar_taxonomia
from evals.juiz.cenarios import (
    DIRETORIO_PADRAO,
    MINIMO_CENARIOS,
    CenarioJuiz,
    carregar_cenarios_juiz,
    selecionar,
)
from evals.juiz.execucao import executar_com_llm
from evals.juiz.julgar import julgar
from evals.juiz.parser import ErroJuiz
from evals.juiz.prompt import VERSAO_PROMPT, hash_prompt
from evals.juiz.regressao import BASELINE_JUIZ, regressoes_juiz
from evals.juiz.relatorio import CHAVE_BASELINE, RelatorioJuiz, ResultadoJuiz

RELATORIOS = Path(__file__).resolve().parent.parent / "relatorios"
MAX_TOKENS_JUIZ = 700

FabricaLLM = Callable[[], Any]


def _parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(prog="python -m evals.juiz", description=__doc__)
    p.add_argument("--cenarios", type=Path, default=DIRETORIO_PADRAO)
    p.add_argument("--ids", default="", help="ids ou nomes de arquivo separados por vírgula")
    p.add_argument("--max", type=int, default=0, help="só os N primeiros (depuração)")
    p.add_argument("--relatorio", type=Path, default=None, help="caminho do .md")
    p.add_argument("--modelo-juiz", default=os.environ.get("JUIZ_MODEL", ""))
    p.add_argument("--baseline", type=Path, default=BASELINE_JUIZ)
    p.add_argument("--tolerancia", type=float, default=None)
    p.add_argument("--sem-executar", action="store_true", help="só valida os cenários")
    p.add_argument(
        "--atualizar-baseline",
        action="store_true",
        help="grava as métricas desta rodada (exige --justificativa)",
    )
    p.add_argument("--justificativa", default=None)
    return p


def _llm_da_sofia() -> Any:
    from src.agent.llm import get_llm

    return get_llm()


def _llm_do_juiz(modelo: str) -> Any:
    from src.agent.llm import _build_chat_openrouter
    from src.config import get_settings

    settings = get_settings()
    return _build_chat_openrouter(
        model=modelo or settings.openrouter_model,
        temperature=0.0,
        max_tokens=MAX_TOKENS_JUIZ,
        settings=settings,
    )


def avaliar_cenario(cenario: CenarioJuiz, llm_sofia: Any, llm_juiz: Any) -> ResultadoJuiz:
    """Executa e julga um cenário; nunca levanta (o problema vai no resultado)."""

    def sem_nota(
        problema: str, detalhe: str, persona: tuple[str, ...] = (), turnos: int = 0
    ) -> ResultadoJuiz:
        return ResultadoJuiz(
            cenario.id,
            cenario.descricao,
            problema=problema,
            detalhe=detalhe,
            falhas_persona=persona,
            turnos=turnos,
        )

    try:
        transcricao = executar_com_llm(cenario, llm_sofia)
    except Exception as exc:  # falha do harness, não do juiz
        return sem_nota("execucao", type(exc).__name__)
    persona = tuple(transcricao.falhas_persona)
    n_turnos = len(transcricao.turnos)
    if transcricao.erro:
        return sem_nota("execucao", transcricao.erro, persona, n_turnos)
    try:
        julgamento = julgar(llm_juiz, cenario, transcricao)
    except ErroJuiz as e:
        return sem_nota("juiz_invalido", str(e)[:160], persona, n_turnos)
    except Exception as exc:  # LLM do juiz fora do ar
        return sem_nota(
            "juiz_invalido", f"juiz indisponível ({type(exc).__name__})", persona, n_turnos
        )
    return ResultadoJuiz(
        cenario.id,
        cenario.descricao,
        notas=julgamento.notas,
        falhas_persona=persona,
        turnos=n_turnos,
    )


def main(
    argv: Sequence[str] | None = None,
    saida_texto: Callable[[str], None] = print,
    fabrica_sofia: FabricaLLM | None = None,
    fabrica_juiz: Callable[[str], Any] | None = None,
) -> int:
    args = _parser().parse_args(argv)
    if args.atualizar_baseline and not args.justificativa:
        saida_texto("erro: --atualizar-baseline exige --justificativa")
        return 2
    try:
        cenarios = carregar_cenarios_juiz(args.cenarios, carregar_taxonomia().categorias)
        ids = [i.strip() for i in args.ids.split(",") if i.strip()]
        cenarios = selecionar(cenarios, ids)
    except ErroFormato as e:
        saida_texto(f"erro: cenário inválido — {e.campo}: {e.motivo}")
        return 2
    if args.max > 0:
        cenarios = cenarios[: args.max]
    if args.sem_executar:
        saida_texto(f"{len(cenarios)} cenário(s) válido(s) em {args.cenarios}")
        if args.cenarios == DIRETORIO_PADRAO and not ids and len(cenarios) < MINIMO_CENARIOS:
            saida_texto(f"erro: o plano pede ≥ {MINIMO_CENARIOS} cenários com juiz")
            return 2
        return 0
    if fabrica_sofia is None and not os.environ.get("OPENROUTER_API_KEY"):
        saida_texto("erro: o juiz exige OPENROUTER_API_KEY (LLM real da Sofia e do juiz)")
        return 2

    llm_sofia = (fabrica_sofia or _llm_da_sofia)()
    llm_juiz = (fabrica_juiz or _llm_do_juiz)(args.modelo_juiz)
    resultados: list[ResultadoJuiz] = []
    for c in cenarios:
        r = avaliar_cenario(c, llm_sofia, llm_juiz)
        resultados.append(r)
        resumo = (
            " ".join(f"{k}={n.nota}" for k, n in r.notas.items()) if r.julgado else f"{r.problema}"
        )
        saida_texto(f"{c.id:<10} {resumo}")

    contexto = {
        "gerado_em": datetime.now(UTC).isoformat(timespec="seconds"),
        "cenários": f"{len(cenarios)} em {args.cenarios.name}",
        "prompt do juiz": f"{VERSAO_PROMPT} (sha256 {hash_prompt()[:12]})",
        "modelo do juiz": args.modelo_juiz or "(o mesmo da Sofia)",
        "JEV": "scriptado por cenário (este eval mede a conversa, não a classificação)",
    }
    relatorio = RelatorioJuiz(tuple(resultados), contexto)
    data = datetime.now(UTC).date().isoformat()
    caminho_md: Path = args.relatorio or (RELATORIOS / f"{data}-juiz.md")
    caminho_md.parent.mkdir(parents=True, exist_ok=True)
    caminho_md.write_text(relatorio.para_markdown(), encoding="utf-8", newline="\n")
    metricas = relatorio.metricas_planas()
    dado = {
        "chave": CHAVE_BASELINE,
        "versao_prompt": VERSAO_PROMPT,
        "contagens": relatorio.contagens(),
        "metricas": metricas,
    }
    caminho_json = caminho_md.with_suffix(".json")
    caminho_json.write_text(
        json.dumps(dado, ensure_ascii=False, indent=2) + "\n", encoding="utf-8", newline="\n"
    )
    saida_texto(f"relatório: {caminho_md}")
    saida_texto(f"métricas:  {caminho_json}")
    for k, v in metricas.items():
        saida_texto(f"  {k:<28} {v:6.1f}")
    contagens = relatorio.contagens()
    saida_texto(
        f"julgados {contagens['julgados']}/{contagens['cenarios']} · "
        f"erros de execução {contagens['erros_execucao']} · juiz inválido {contagens['juiz_invalido']}"
    )

    baseline: dict[str, Any] = {"versao": 1, "tolerancia_pontos": 2.0, "metricas": {}}
    if args.baseline.exists():
        baseline = json.loads(args.baseline.read_text(encoding="utf-8"))
    if args.atualizar_baseline:
        if contagens["julgados"] < len(cenarios):
            saida_texto("erro: --atualizar-baseline recusado — há cenário sem nota nesta rodada")
            return 2
        baseline.setdefault("metricas", {})[CHAVE_BASELINE] = metricas
        baseline["atualizado_em"] = datetime.now(UTC).date().isoformat()
        baseline["justificativa"] = args.justificativa
        baseline["versao_prompt"] = VERSAO_PROMPT
        args.baseline.write_text(
            json.dumps(baseline, ensure_ascii=False, indent=2) + "\n",
            encoding="utf-8",
            newline="\n",
        )
        saida_texto(f"baseline atualizado: {args.baseline} [{CHAVE_BASELINE}]")
        return 0
    regressoes = regressoes_juiz(dado, baseline, args.tolerancia)
    if regressoes:
        saida_texto("REGRESSÃO contra o baseline do juiz:")
        for reg in regressoes:
            saida_texto(
                f"  - {reg.metrica}: {reg.base:.2f} → {reg.atual:.2f} ({reg.piora:+.2f} pts)"
            )
        return 1
    if (baseline.get("metricas") or {}).get(CHAVE_BASELINE):
        saida_texto("sem regressão contra o baseline do juiz")
    else:
        saida_texto("baseline do juiz sem métricas: nada a comparar")
    return 0


if __name__ == "__main__":
    _reconfigurar = getattr(sys.stdout, "reconfigure", None)
    if _reconfigurar is not None:
        _reconfigurar(encoding="utf-8")
    sys.exit(main())
