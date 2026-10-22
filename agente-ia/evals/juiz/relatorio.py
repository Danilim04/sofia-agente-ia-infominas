# mypy: disallow-untyped-defs, disallow-incomplete-defs, check-untyped-defs, disallow-any-generics, warn-return-any, strict-equality
"""Agregação das notas do juiz e relatório (Markdown + métricas planas).

Métricas (todas de 0 a 100, maior é melhor, para o mesmo `+/- 2 pts` da classificação):

- `juiz.<critério>.media`: média das notas do critério, `nota / 5 * 100`, só nos cenários
  julgados em que o critério se aplica;
- `juiz.geral.media`: média de todas as notas;
- `juiz.aprovacao`: % de notas ≥ 4 (`NOTA_APROVACAO`);
- cenário com erro de execução ou resposta inválida do juiz NÃO entra nas médias (não
  recebe nota inventada); aparece em `erros_execucao` / `juiz_invalido` e no relatório.

O juiz é um LLM: a nota oscila de uma noite para outra. A tolerância de 2 pts vem do
plano; com ~26 cenários, trate uma queda isolada de ~2 pts como ruído e uma queda
repetida (ou de um critério inteiro) como regressão.
"""

from __future__ import annotations

from collections.abc import Mapping, Sequence
from dataclasses import dataclass, field
from typing import Final

from evals.juiz.cenarios import CRITERIOS
from evals.juiz.parser import NOTA_APROVACAO, NOTA_MAX, NotaCriterio

CHAVE_BASELINE: Final[str] = "juiz/noturno"


@dataclass(frozen=True)
class ResultadoJuiz:
    cenario_id: str
    descricao: str
    notas: Mapping[str, NotaCriterio] = field(default_factory=dict)
    #: "execucao" (o grafo/LLM falhou) | "juiz_invalido" | "" (julgado)
    problema: str = ""
    detalhe: str = ""
    falhas_persona: tuple[str, ...] = ()
    turnos: int = 0

    @property
    def julgado(self) -> bool:
        return not self.problema


@dataclass(frozen=True)
class RelatorioJuiz:
    resultados: tuple[ResultadoJuiz, ...]
    contexto: Mapping[str, str] = field(default_factory=dict)

    @property
    def julgados(self) -> list[ResultadoJuiz]:
        return [r for r in self.resultados if r.julgado]

    def metricas_planas(self) -> dict[str, float]:
        planas: dict[str, float] = {}
        todas: list[NotaCriterio] = []
        for criterio in CRITERIOS:
            notas = [r.notas[criterio] for r in self.julgados if criterio in r.notas]
            if notas:
                planas[f"juiz.{criterio}.media"] = round(
                    sum(n.pontos for n in notas) / len(notas), 2
                )
                todas.extend(notas)
        if todas:
            planas["juiz.geral.media"] = round(sum(n.pontos for n in todas) / len(todas), 2)
            planas["juiz.aprovacao"] = round(
                100.0 * sum(1 for n in todas if n.nota >= NOTA_APROVACAO) / len(todas), 2
            )
        return planas

    def contagens(self) -> dict[str, int]:
        return {
            "cenarios": len(self.resultados),
            "julgados": len(self.julgados),
            "erros_execucao": sum(1 for r in self.resultados if r.problema == "execucao"),
            "juiz_invalido": sum(1 for r in self.resultados if r.problema == "juiz_invalido"),
        }

    def para_markdown(self) -> str:
        c = self.contagens()
        linhas = ["# Evals noturnos — conversa com LLM real + juiz", ""]
        linhas.extend(f"- **{k}**: {v}" for k, v in self.contexto.items())
        linhas += [
            "",
            f"Cenários: **{c['cenarios']}** · julgados: **{c['julgados']}** · "
            f"erros de execução: {c['erros_execucao']} · resposta inválida do juiz: {c['juiz_invalido']}",
            "",
            "> Notas de 0 a 5 dadas por um LLM juiz; as médias são `nota/5*100`. O juiz oscila de "
            "uma noite para outra: uma queda isolada de ~2 pts é ruído, queda repetida não.",
            "",
        ]
        metricas = self.metricas_planas()
        if metricas:
            linhas += ["## Médias (0–100)", "", "| métrica | valor |", "|---|---:|"]
            linhas += [f"| {k} | {v:.1f} |" for k, v in metricas.items()]
            linhas.append("")
        linhas += [
            "## Cenários",
            "",
            "| cenário | " + " | ".join(CRITERIOS) + " | situação |",
            "|---|" + "---:|" * len(CRITERIOS) + "---|",
        ]
        for r in self.resultados:
            celulas = [str(r.notas[k].nota) if k in r.notas else "—" for k in CRITERIOS]
            situacao = "julgado" if r.julgado else f"{r.problema}: {r.detalhe}"
            if r.falhas_persona:
                situacao += f" · persona (regras): {len(r.falhas_persona)}"
            linhas.append(
                f"| {r.cenario_id} — {r.descricao} | " + " | ".join(celulas) + f" | {situacao} |"
            )
        baixas = [
            (r, k, n) for r in self.julgados for k, n in r.notas.items() if n.nota < NOTA_APROVACAO
        ]
        if baixas:
            linhas += ["", f"## Notas abaixo de {NOTA_APROVACAO}", ""]
            for r, k, n in baixas:
                linhas.append(
                    f"- **{r.cenario_id}** `{k}` = {n.nota}/{NOTA_MAX}: {n.justificativa}"
                )
        com_persona = [r for r in self.resultados if r.falhas_persona]
        if com_persona:
            linhas += ["", "## Violações de persona achadas pelas regras (sem juiz)", ""]
            for r in com_persona:
                linhas.extend(f"- **{r.cenario_id}**: {f}" for f in r.falhas_persona)
        return "\n".join(linhas) + "\n"


def media(valores: Sequence[float]) -> float:
    return sum(valores) / len(valores) if valores else 0.0
