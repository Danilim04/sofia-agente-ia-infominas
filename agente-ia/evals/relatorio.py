# mypy: disallow-untyped-defs, disallow-incomplete-defs, check-untyped-defs, disallow-any-generics, warn-return-any, strict-equality
"""Métricas de classificação e relatório em Markdown (docs/plano/02 "Dataset").

Entrada: uma lista de `ResultadoCaso`, cada um com as perguntas avaliadas
(`ParPrevisao`: esperado, previsto, confiança, acerto) e a latência. Saída:
`Relatorio` com, por pergunta, acurácia, macro-F1, cobertura@limiar,
acurácia@limiar, calibração em 10 bins + ECE e matriz de confusão; latência
p50/p95 global; `para_markdown()` e `metricas_planas()` (para o baseline).

Convenções (todas determinísticas, sem dependência de numpy/sklearn):

- **macro-F1**: média simples do F1 das classes presentes em esperado ∪
  previsto; classe sem verdadeiro-positivo tem F1 = 0. Só para perguntas
  `exata=True` (categoria, intenção); perguntas ordinais com piso
  (`prioridade ≥ prioridade_min`) entram só em acurácia.
- **cobertura@limiar**: fração dos casos com `confianca ≥ limiar` (o que o
  JEV decidiria sem fallback). **acurácia@limiar**: acurácia só nesses.
- **calibração**: 10 bins de confiança [0,0.1), …, [0.9,1.0]; ECE =
  Σ (n_b/N)·|acc_b − conf_b|. Só para perguntas exatas: numa ordinal com piso a
  confiança é do score exato e o acerto é "≥ piso", então o ECE não significa nada.
- **percentil**: método *nearest-rank* (posição ⌈p·n⌉ da lista ordenada).
- **pipeline**: taxas e médias dos `extras` de cada caso (fração de turnos com
  fallback LLM, com esclarecimento, com JEV fora do ar…): `bool` vira taxa,
  número vira média. Contexto do relatório, não critério de aceite.
- **comparar_janelas**: uma linha por janela de contexto (C-JEV1: 1/3/6/10
  mensagens) com as métricas que decidem a janela.
"""

from __future__ import annotations

import math
from collections.abc import Iterable, Mapping, Sequence
from dataclasses import dataclass, field

N_BINS = 10


@dataclass(frozen=True)
class ParPrevisao:
    """Uma pergunta avaliada em um caso."""

    esperado: str
    previsto: str
    confianca: float
    acerto: bool
    exata: bool = True


@dataclass(frozen=True)
class ResultadoCaso:
    id: str
    perguntas: Mapping[str, ParPrevisao]
    latencia_ms: float
    #: Observações do pipeline neste caso: `bool` (fallback usado, esclarecimento…)
    #: ou número (tamanho do estado enviado). Agregadas em `Relatorio.pipeline`.
    extras: Mapping[str, float | bool] = field(default_factory=dict)


@dataclass(frozen=True)
class BinCalibracao:
    inferior: float
    superior: float
    n: int
    confianca_media: float
    acuracia: float


@dataclass(frozen=True)
class MetricasPergunta:
    pergunta: str
    n: int
    acuracia: float
    macro_f1: float | None
    limiar: float
    cobertura_no_limiar: float
    acuracia_no_limiar: float | None
    ece: float | None  # None para perguntas ordinais (calibração não se aplica)
    calibracao: tuple[BinCalibracao, ...]
    classes: tuple[str, ...]
    confusao: Mapping[str, Mapping[str, int]]  # esperado -> previsto -> contagem
    f1_por_classe: Mapping[str, float] = field(default_factory=dict)


@dataclass(frozen=True)
class Relatorio:
    titulo: str
    n_casos: int
    perguntas: tuple[MetricasPergunta, ...]
    latencia_p50_ms: float
    latencia_p95_ms: float
    avisos: tuple[str, ...] = ()
    contexto: Mapping[str, str] = field(default_factory=dict)
    #: Taxa (0–1) por extra booleano, média por extra numérico.
    pipeline: Mapping[str, float] = field(default_factory=dict)
    #: Faixa no topo do relatório (ex.: "exemplo offline com fakes, não é medição real").
    banner: str = ""
    #: Blocos Markdown já formatados (ex.: comparação de janelas), antes do detalhe.
    secoes_extra: tuple[str, ...] = ()

    def metricas_planas(self) -> dict[str, float]:
        """`{"categoria.macro_f1": 0.91, ...}` em pontos percentuais (0–100)."""
        planas: dict[str, float] = {}
        for p in self.perguntas:
            planas[f"{p.pergunta}.acuracia"] = _pct(p.acuracia)
            if p.macro_f1 is not None:
                planas[f"{p.pergunta}.macro_f1"] = _pct(p.macro_f1)
            planas[f"{p.pergunta}.cobertura_no_limiar"] = _pct(p.cobertura_no_limiar)
            if p.acuracia_no_limiar is not None:
                planas[f"{p.pergunta}.acuracia_no_limiar"] = _pct(p.acuracia_no_limiar)
            if p.ece is not None:
                planas[f"{p.pergunta}.ece"] = _pct(p.ece)
        planas["latencia.p50_ms"] = round(self.latencia_p50_ms, 1)
        planas["latencia.p95_ms"] = round(self.latencia_p95_ms, 1)
        return planas

    def para_markdown(self) -> str:
        linhas: list[str] = [f"# {self.titulo}", ""]
        if self.banner:
            linhas.extend([f"> **{self.banner}**", ""])
        if self.contexto:
            linhas.extend(f"- **{k}**: {v}" for k, v in self.contexto.items())
            linhas.append("")
        linhas.append(
            f"Casos avaliados: **{self.n_casos}** · latência p50 "
            f"{self.latencia_p50_ms:.0f} ms · p95 {self.latencia_p95_ms:.0f} ms"
        )
        linhas.append("")
        for aviso in self.avisos:
            linhas.append(f"> ⚠️ {aviso}")
        if self.avisos:
            linhas.append("")
        for secao in self.secoes_extra:
            linhas.extend([secao.rstrip(), ""])
        linhas.append("## Resumo por pergunta")
        linhas.append("")
        linhas.append(
            "| pergunta | n | acurácia | macro-F1 | limiar | cobertura@limiar | acurácia@limiar | ECE |"
        )
        linhas.append("|---|---:|---:|---:|---:|---:|---:|---:|")
        for p in self.perguntas:
            linhas.append(
                f"| {p.pergunta} | {p.n} | {_fmt(p.acuracia)} | {_fmt(p.macro_f1)} | "
                f"{p.limiar:.2f} | {_fmt(p.cobertura_no_limiar)} | {_fmt(p.acuracia_no_limiar)} | "
                f"{_ece(p.ece)} |"
            )
        linhas.append("")
        if self.pipeline:
            linhas.append("## Pipeline (o que o roteador fez)")
            linhas.append("")
            linhas.append("| observação | valor |")
            linhas.append("|---|---:|")
            for nome, valor in sorted(self.pipeline.items()):
                linhas.append(f"| {nome} | {_fmt_pipeline(nome, valor)} |")
            linhas.append("")
        for p in self.perguntas:
            linhas.append(f"## {p.pergunta}")
            linhas.append("")
            if p.f1_por_classe:
                linhas.append("### F1 por classe")
                linhas.append("")
                linhas.append("| classe | F1 |")
                linhas.append("|---|---:|")
                for classe in p.classes:
                    linhas.append(f"| {classe} | {_fmt(p.f1_por_classe.get(classe))} |")
                linhas.append("")
            if not p.calibracao:
                continue
            linhas.append("### Calibração (10 bins)")
            linhas.append("")
            linhas.append("| bin | n | confiança média | acurácia |")
            linhas.append("|---|---:|---:|---:|")
            for b in p.calibracao:
                if b.n == 0:
                    continue
                linhas.append(
                    f"| [{b.inferior:.1f}, {b.superior:.1f}{']' if b.superior >= 1 else ')'} | "
                    f"{b.n} | {b.confianca_media:.3f} | {_fmt(b.acuracia)} |"
                )
            linhas.append("")
            if p.confusao:
                linhas.append("### Matriz de confusão (linha = esperado, coluna = previsto)")
                linhas.append("")
                linhas.append("| esperado \\ previsto | " + " | ".join(p.classes) + " |")
                linhas.append("|---|" + "---:|" * len(p.classes))
                for esperado in p.classes:
                    linha = p.confusao.get(esperado, {})
                    linhas.append(
                        f"| {esperado} | "
                        + " | ".join(str(linha.get(prev, 0)) for prev in p.classes)
                        + " |"
                    )
                linhas.append("")
        return "\n".join(linhas).rstrip() + "\n"


def _pct(v: float) -> float:
    return round(100.0 * v, 2)


def _fmt(v: float | None) -> str:
    return "—" if v is None else f"{100.0 * v:.1f}%"


def _fmt_pipeline(nome: str, valor: float) -> str:
    """Taxas (`taxa_*`) em %, o resto como número."""
    return f"{100.0 * valor:.1f}%" if nome.startswith("taxa_") else f"{valor:.1f}"


def _ece(v: float | None) -> str:
    """ECE é fração (critério do 02: ≤ 0.08), não percentual."""
    return "—" if v is None else f"{v:.3f}"


# -------------------------------------------------------------------- métricas
def acuracia(acertos: Sequence[bool]) -> float:
    return sum(acertos) / len(acertos) if acertos else 0.0


def f1_por_classe(pares: Sequence[tuple[str, str]]) -> dict[str, float]:
    """F1 de cada classe presente em esperado ∪ previsto."""
    classes = sorted({e for e, _ in pares} | {p for _, p in pares})
    f1s: dict[str, float] = {}
    for c in classes:
        vp = sum(1 for e, p in pares if e == c and p == c)
        fp = sum(1 for e, p in pares if e != c and p == c)
        fn = sum(1 for e, p in pares if e == c and p != c)
        denom = 2 * vp + fp + fn
        f1s[c] = (2 * vp / denom) if denom else 0.0
    return f1s


def macro_f1(pares: Sequence[tuple[str, str]]) -> float:
    f1s = f1_por_classe(pares)
    return sum(f1s.values()) / len(f1s) if f1s else 0.0


def matriz_confusao(pares: Sequence[tuple[str, str]]) -> dict[str, dict[str, int]]:
    matriz: dict[str, dict[str, int]] = {}
    for esperado, previsto in pares:
        linha = matriz.setdefault(esperado, {})
        linha[previsto] = linha.get(previsto, 0) + 1
    return matriz


def cobertura_e_acuracia_no_limiar(
    pares: Sequence[tuple[float, bool]], limiar: float
) -> tuple[float, float | None]:
    """(cobertura, acurácia dos cobertos ou None se nenhum coberto)."""
    if not pares:
        return 0.0, None
    cobertos = [acerto for conf, acerto in pares if conf >= limiar]
    cobertura = len(cobertos) / len(pares)
    return cobertura, (acuracia(cobertos) if cobertos else None)


def calibracao(
    pares: Sequence[tuple[float, bool]], n_bins: int = N_BINS
) -> tuple[list[BinCalibracao], float]:
    """Bins de confiança de largura igual e ECE. Confiança 1.0 cai no último bin."""
    largura = 1.0 / n_bins
    baldes: list[list[tuple[float, bool]]] = [[] for _ in range(n_bins)]
    for conf, acerto in pares:
        idx = min(int(conf / largura), n_bins - 1)
        baldes[idx].append((conf, acerto))
    bins: list[BinCalibracao] = []
    ece = 0.0
    total = len(pares)
    for i, balde in enumerate(baldes):
        n = len(balde)
        conf_media = sum(c for c, _ in balde) / n if n else 0.0
        acc = acuracia([a for _, a in balde]) if n else 0.0
        bins.append(
            BinCalibracao(round(i * largura, 2), round((i + 1) * largura, 2), n, conf_media, acc)
        )
        if n and total:
            ece += (n / total) * abs(acc - conf_media)
    return bins, ece


def percentil(valores: Iterable[float], p: float) -> float:
    """Percentil *nearest-rank* (p em 0–100). Lista vazia → 0.0."""
    ordenados = sorted(valores)
    if not ordenados:
        return 0.0
    posicao = max(1, math.ceil(p / 100.0 * len(ordenados)))
    return ordenados[posicao - 1]


# ------------------------------------------------------------------- relatório
def metricas_pergunta(
    pergunta: str, pares: Sequence[ParPrevisao], limiar: float
) -> MetricasPergunta:
    acertos = [p.acerto for p in pares]
    conf_acerto = [(p.confianca, p.acerto) for p in pares]
    exata = all(p.exata for p in pares) if pares else False
    rotulos = [(p.esperado, p.previsto) for p in pares]
    cobertura, acc_limiar = cobertura_e_acuracia_no_limiar(conf_acerto, limiar)
    bins, ece = calibracao(conf_acerto) if exata else ([], None)
    f1s = f1_por_classe(rotulos) if exata else {}
    classes = tuple(sorted(f1s)) if exata else ()
    return MetricasPergunta(
        pergunta=pergunta,
        n=len(pares),
        acuracia=acuracia(acertos),
        macro_f1=(sum(f1s.values()) / len(f1s)) if f1s else None,
        limiar=limiar,
        cobertura_no_limiar=cobertura,
        acuracia_no_limiar=acc_limiar,
        ece=ece,
        calibracao=tuple(bins),
        classes=classes,
        confusao=matriz_confusao(rotulos) if exata else {},
        f1_por_classe=f1s,
    )


def gerar_relatorio(
    resultados: Sequence[ResultadoCaso],
    limiares: Mapping[str, float],
    titulo: str = "Evals de classificação",
    avisos: Sequence[str] = (),
    contexto: Mapping[str, str] | None = None,
    limiar_padrao: float = 0.0,
) -> Relatorio:
    """Agrupa os pares por pergunta e calcula tudo. Perguntas ausentes num caso são ignoradas nele."""
    por_pergunta: dict[str, list[ParPrevisao]] = {}
    for r in resultados:
        for nome, par in r.perguntas.items():
            por_pergunta.setdefault(nome, []).append(par)
    perguntas = tuple(
        metricas_pergunta(nome, pares, limiares.get(nome, limiar_padrao))
        for nome, pares in sorted(por_pergunta.items())
    )
    latencias = [r.latencia_ms for r in resultados]
    return Relatorio(
        titulo=titulo,
        n_casos=len(resultados),
        perguntas=perguntas,
        latencia_p50_ms=percentil(latencias, 50),
        latencia_p95_ms=percentil(latencias, 95),
        avisos=tuple(avisos),
        contexto=dict(contexto or {}),
        pipeline=agregar_pipeline(resultados),
    )


def agregar_pipeline(resultados: Sequence[ResultadoCaso]) -> dict[str, float]:
    """`taxa_<extra>` para extras booleanos, `media_<extra>` para numéricos."""
    por_extra: dict[str, list[float | bool]] = {}
    for r in resultados:
        for nome, valor in r.extras.items():
            por_extra.setdefault(nome, []).append(valor)
    agregado: dict[str, float] = {}
    for nome, valores in sorted(por_extra.items()):
        if all(isinstance(v, bool) for v in valores):
            agregado[f"taxa_{nome}"] = sum(1 for v in valores if v) / len(valores)
        else:
            agregado[f"media_{nome}"] = sum(float(v) for v in valores) / len(valores)
    return agregado


def comparar_janelas(relatorios: Mapping[int, Relatorio]) -> str:
    """Tabela Markdown: uma linha por janela de contexto (C-JEV1), com o que decide a janela.

    Marca em negrito a melhor janela por macro-F1 de categoria (empate → a menor
    janela, que custa menos tokens). É insumo para a decisão, não a decisão.
    """

    def metrica(r: Relatorio, pergunta: str, atributo: str) -> float | None:
        for p in r.perguntas:
            if p.pergunta == pergunta:
                valor = getattr(p, atributo)
                return float(valor) if valor is not None else None
        return None

    melhor: int | None = None
    melhor_f1 = -1.0
    for janela in sorted(relatorios):
        f1 = metrica(relatorios[janela], "categoria", "macro_f1")
        if f1 is not None and f1 > melhor_f1:
            melhor, melhor_f1 = janela, f1

    linhas = [
        "## Comparação de janelas de contexto (C-JEV1)",
        "",
        "| janela (msgs) | intenção acurácia | categoria acurácia | categoria macro-F1 | "
        "categoria cobertura@limiar | categoria acurácia@limiar | categoria ECE | "
        "estado médio (caracteres) | latência p50 (ms) |",
        "|---|---:|---:|---:|---:|---:|---:|---:|---:|",
    ]
    for janela in sorted(relatorios):
        r = relatorios[janela]
        rotulo = f"**{janela}**" if janela == melhor else str(janela)
        ece = metrica(r, "categoria", "ece")
        linhas.append(
            f"| {rotulo} | {_fmt(metrica(r, 'intencao', 'acuracia'))} | "
            f"{_fmt(metrica(r, 'categoria', 'acuracia'))} | "
            f"{_fmt(metrica(r, 'categoria', 'macro_f1'))} | "
            f"{_fmt(metrica(r, 'categoria', 'cobertura_no_limiar'))} | "
            f"{_fmt(metrica(r, 'categoria', 'acuracia_no_limiar'))} | {_ece(ece)} | "
            f"{r.pipeline.get('media_estado_caracteres', 0.0):.0f} | {r.latencia_p50_ms:.0f} |"
        )
    linhas.append("")
    linhas.append(
        "Negrito = melhor macro-F1 de categoria. Janela maior custa mais tokens (e latência) e "
        "só vale se subir as métricas; o `.env` usa `JEV_JANELA_MSGS`. Decidir com o dataset "
        "histórico (P9), não com o sintético."
    )
    return "\n".join(linhas)


# -------------------------------------------------------------------- baseline
METRICAS_BLOQUEANTES: tuple[str, ...] = ("macro_f1", "acuracia_no_limiar")
METRICAS_MENOR_MELHOR: tuple[str, ...] = ("ece",)


def comparar_com_baseline(
    atuais: Mapping[str, float], baseline: Mapping[str, float], tolerancia_pontos: float = 2.0
) -> list[str]:
    """Regressões (> tolerância, em pontos) nas métricas bloqueantes presentes no baseline.

    macro-F1 e acurácia@limiar: queda é regressão. ECE: aumento é regressão.

    Só compara chaves que existem nos dois lados: baseline vazio nunca falha.
    """
    regressoes: list[str] = []
    for chave, valor_base in sorted(baseline.items()):
        maior_melhor = any(chave.endswith("." + m) for m in METRICAS_BLOQUEANTES)
        menor_melhor = any(chave.endswith("." + m) for m in METRICAS_MENOR_MELHOR)
        if not (maior_melhor or menor_melhor):
            continue
        atual = atuais.get(chave)
        if atual is None:
            continue
        piora = (valor_base - atual) if maior_melhor else (atual - valor_base)
        if piora > tolerancia_pontos:
            sinal = "−" if maior_melhor else "+"
            regressoes.append(f"{chave}: {valor_base:.2f} → {atual:.2f} ({sinal}{piora:.2f} pts)")
    return regressoes
