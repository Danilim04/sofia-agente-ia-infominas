"""Métricas com números conhecidos (calculados à mão nos comentários)."""

from __future__ import annotations

import pytest

from evals import relatorio as rel
from evals.relatorio import ParPrevisao, ResultadoCaso

# esperado→previsto: A→A, A→A, A→B, B→B, B→A, C→C
PARES = [("A", "A"), ("A", "A"), ("A", "B"), ("B", "B"), ("B", "A"), ("C", "C")]
CONF = [0.9, 0.8, 0.6, 0.95, 0.5, 0.75]
ACERTOS = [e == p for e, p in PARES]  # T T F T F T


def test_acuracia():
    assert rel.acuracia(ACERTOS) == pytest.approx(4 / 6)
    assert rel.acuracia([]) == 0.0


def test_f1_por_classe_e_macro():
    # A: vp=2 fp=1 fn=1 → 2·2/(4+1+1)=0.6667 · B: vp=1 fp=1 fn=1 → 0.5 · C: 1.0
    f1 = rel.f1_por_classe(PARES)
    assert f1 == {"A": pytest.approx(2 / 3), "B": pytest.approx(0.5), "C": pytest.approx(1.0)}
    assert rel.macro_f1(PARES) == pytest.approx((2 / 3 + 0.5 + 1.0) / 3)


def test_macro_f1_inclui_classe_so_prevista():
    # esperado só A; previsto A e Z → Z tem F1 0 e entra na média
    assert rel.macro_f1([("A", "A"), ("A", "Z")]) == pytest.approx((2 / 3 + 0.0) / 2)
    assert rel.macro_f1([]) == 0.0


def test_matriz_confusao():
    m = rel.matriz_confusao(PARES)
    assert m == {"A": {"A": 2, "B": 1}, "B": {"B": 1, "A": 1}, "C": {"C": 1}}


def test_cobertura_e_acuracia_no_limiar():
    pares = list(zip(CONF, ACERTOS, strict=True))
    # conf ≥ 0.7: 0.9T 0.8T 0.95T 0.75T → cobertura 4/6, acurácia 1.0
    assert rel.cobertura_e_acuracia_no_limiar(pares, 0.7) == (
        pytest.approx(4 / 6),
        pytest.approx(1.0),
    )
    # conf ≥ 0.5: todos → cobertura 1, acurácia 4/6
    assert rel.cobertura_e_acuracia_no_limiar(pares, 0.5) == (
        pytest.approx(1.0),
        pytest.approx(4 / 6),
    )
    # ninguém coberto → acurácia None
    assert rel.cobertura_e_acuracia_no_limiar(pares, 0.99) == (0.0, None)
    assert rel.cobertura_e_acuracia_no_limiar([], 0.5) == (0.0, None)


def test_calibracao_e_ece():
    # bin [0.9,1.0]: 2 casos conf 0.95 acc 1.0 → |1−0.95|·2/4 = 0.025
    # bin [0.8,0.9): 2 casos conf 0.85 acc 0.5 → |0.5−0.85|·2/4 = 0.175  → ECE 0.2
    bins, ece = rel.calibracao([(0.95, True), (0.95, True), (0.85, False), (0.85, True)])
    assert len(bins) == 10
    assert ece == pytest.approx(0.2)
    ultimo, penultimo = bins[9], bins[8]
    assert (ultimo.n, ultimo.confianca_media, ultimo.acuracia) == (2, pytest.approx(0.95), 1.0)
    assert (penultimo.n, penultimo.confianca_media, penultimo.acuracia) == (
        2,
        pytest.approx(0.85),
        0.5,
    )
    assert bins[0].n == 0


def test_calibracao_confianca_um_cai_no_ultimo_bin():
    bins, ece = rel.calibracao([(1.0, True), (0.0, False)])
    assert bins[9].n == 1 and bins[0].n == 1
    assert ece == pytest.approx(0.0)


def test_percentil_nearest_rank():
    dez = [10, 20, 30, 40, 50, 60, 70, 80, 90, 100]
    assert rel.percentil(dez, 50) == 50
    assert rel.percentil(dez, 95) == 100
    assert rel.percentil(range(1, 21), 95) == 19  # ⌈0.95·20⌉ = 19
    assert rel.percentil([7], 95) == 7
    assert rel.percentil([], 50) == 0.0


def _resultados() -> list[ResultadoCaso]:
    out = []
    for i, ((e, p), c) in enumerate(zip(PARES, CONF, strict=True)):
        perguntas = {"categoria": ParPrevisao(e, p, c, e == p)}
        if i % 2 == 0:  # prioridade ordinal só em metade dos casos
            perguntas["prioridade"] = ParPrevisao("media", "alta", 0.9, True, exata=False)
        out.append(ResultadoCaso(id=f"c{i}", perguntas=perguntas, latencia_ms=float(10 * (i + 1))))
    return out


def test_gerar_relatorio_agrega_por_pergunta():
    r = rel.gerar_relatorio(_resultados(), {"categoria": 0.7}, titulo="t", avisos=["placeholder"])
    assert r.n_casos == 6
    por_nome = {p.pergunta: p for p in r.perguntas}
    cat = por_nome["categoria"]
    assert cat.n == 6
    assert cat.acuracia == pytest.approx(4 / 6)
    assert cat.macro_f1 == pytest.approx((2 / 3 + 0.5 + 1.0) / 3)
    assert cat.limiar == 0.7
    assert cat.cobertura_no_limiar == pytest.approx(4 / 6)
    assert cat.acuracia_no_limiar == pytest.approx(1.0)
    assert cat.classes == ("A", "B", "C")
    pri = por_nome["prioridade"]
    assert pri.n == 3 and pri.acuracia == 1.0
    assert pri.macro_f1 is None and pri.confusao == {}  # ordinal: sem F1/confusão
    assert pri.ece is None and pri.calibracao == ()  # ordinal: sem calibração
    assert cat.ece is not None
    assert pri.limiar == 0.0  # sem limiar informado → padrão
    # latências 10..60: p50 = 30 (⌈3⌉), p95 = 60 (⌈5.7⌉=6)
    assert (r.latencia_p50_ms, r.latencia_p95_ms) == (30.0, 60.0)


def test_metricas_planas_em_pontos_percentuais():
    r = rel.gerar_relatorio(_resultados(), {"categoria": 0.7})
    planas = r.metricas_planas()
    assert planas["categoria.acuracia"] == pytest.approx(66.67, abs=0.01)
    assert planas["categoria.macro_f1"] == pytest.approx(72.22, abs=0.01)
    assert planas["categoria.cobertura_no_limiar"] == pytest.approx(66.67, abs=0.01)
    assert planas["categoria.acuracia_no_limiar"] == 100.0
    assert "prioridade.macro_f1" not in planas
    assert "prioridade.ece" not in planas
    assert "categoria.ece" in planas
    assert planas["latencia.p95_ms"] == 60.0


def test_markdown_tem_tabelas_e_avisos():
    md = rel.gerar_relatorio(
        _resultados(), {"categoria": 0.7}, titulo="Teste", avisos=["cuidado"]
    ).para_markdown()
    assert md.startswith("# Teste")
    assert "> ⚠️ cuidado" in md
    assert "| categoria | 6 | 66.7% | 72.2% | 0.70 | 66.7% | 100.0% | 0." in md  # ECE: fração
    assert "Matriz de confusão" in md
    assert "| A | 2 | 1 | 0 |" in md
    assert "| prioridade | 3 | 100.0% | — | 0.00 | 100.0% | 100.0% | — |" in md
    assert md.count("### Calibração (10 bins)") == 1  # só a pergunta exata


def test_comparar_com_baseline_ece_menor_e_melhor():
    base = {"categoria.ece": 5.0}
    assert rel.comparar_com_baseline({"categoria.ece": 7.5}, base, 2.0) == [
        "categoria.ece: 5.00 → 7.50 (+2.50 pts)"
    ]
    assert rel.comparar_com_baseline({"categoria.ece": 3.0}, base, 2.0) == []  # melhorou
    assert rel.comparar_com_baseline({"categoria.ece": 6.5}, base, 2.0) == []  # na tolerância


def test_comparar_com_baseline():
    base = {
        "categoria.macro_f1": 90.0,
        "categoria.acuracia_no_limiar": 95.0,
        "categoria.acuracia": 90.0,
    }
    # queda de 2.5 em macro_f1 (bloqueante), 1.0 em acurácia@limiar (ok), 40 em acurácia (não bloqueante)
    atuais = {
        "categoria.macro_f1": 87.5,
        "categoria.acuracia_no_limiar": 94.0,
        "categoria.acuracia": 50.0,
    }
    regs = rel.comparar_com_baseline(atuais, base, 2.0)
    assert regs == ["categoria.macro_f1: 90.00 → 87.50 (−2.50 pts)"]
    assert rel.comparar_com_baseline(atuais, {}, 2.0) == []
    assert rel.comparar_com_baseline({}, base, 2.0) == []  # métrica ausente não conta
    assert rel.comparar_com_baseline(atuais, base, 3.0) == []


# ------------------------------------------------------------------ E2.4: pipeline e janelas
def _resultado_com_extras(extras: dict[str, float | bool]) -> ResultadoCaso:
    return ResultadoCaso(id="x", perguntas={}, latencia_ms=1.0, extras=extras)


def test_agregar_pipeline_taxa_para_bool_e_media_para_numero():
    resultados = [
        _resultado_com_extras({"fallback_chamado": True, "estado_caracteres": 40.0}),
        _resultado_com_extras({"fallback_chamado": False, "estado_caracteres": 60.0}),
        _resultado_com_extras({"fallback_chamado": False, "estado_caracteres": 80.0}),
        _resultado_com_extras({"fallback_chamado": False, "estado_caracteres": 20.0}),
    ]
    # 1 de 4 chamou o fallback; (40+60+80+20)/4 = 50
    assert rel.agregar_pipeline(resultados) == {
        "taxa_fallback_chamado": pytest.approx(0.25),
        "media_estado_caracteres": pytest.approx(50.0),
    }
    assert rel.agregar_pipeline([]) == {}


def test_markdown_mostra_banner_pipeline_e_secoes_extra():
    r = rel.gerar_relatorio(
        [
            _resultado_com_extras({"esclarecimento": True}),
            _resultado_com_extras({"esclarecimento": False}),
        ],
        {},
        titulo="t",
    )
    from dataclasses import replace

    texto = replace(
        r,
        banner="exemplo offline com fakes, não é medição real",
        secoes_extra=("## Extra\n\nlinha",),
    ).para_markdown()
    assert texto.splitlines()[2] == "> **exemplo offline com fakes, não é medição real**"
    assert "## Extra" in texto
    assert "| taxa_esclarecimento | 50.0% |" in texto


def _relatorio_com_f1(macro_f1_categoria: float, estado_medio: float = 100.0) -> rel.Relatorio:
    # n=10 casos: `acertos` de 10 em A→A (resto A→B) dá macro-F1 conhecido; aqui só montamos o valor direto.
    pergunta = rel.MetricasPergunta(
        pergunta="categoria",
        n=10,
        acuracia=macro_f1_categoria,
        macro_f1=macro_f1_categoria,
        limiar=0.65,
        cobertura_no_limiar=0.5,
        acuracia_no_limiar=0.9,
        ece=0.1,
        calibracao=(),
        classes=(),
        confusao={},
    )
    return rel.Relatorio(
        titulo="t",
        n_casos=10,
        perguntas=(pergunta,),
        latencia_p50_ms=200.0,
        latencia_p95_ms=300.0,
        pipeline={"media_estado_caracteres": estado_medio},
    )


def test_comparar_janelas_marca_a_melhor_e_desempata_pela_menor():
    tabela = rel.comparar_janelas(
        {
            10: _relatorio_com_f1(0.8),
            1: _relatorio_com_f1(0.6),
            3: _relatorio_com_f1(0.8),
            6: _relatorio_com_f1(0.7),
        }
    )
    linhas = [
        linha for linha in tabela.splitlines() if linha.startswith("| ") and "---" not in linha
    ]
    rotulos = [linha.split("|")[1].strip() for linha in linhas[1:]]
    assert rotulos == ["1", "**3**", "6", "10"]  # ordem crescente; empate 3×10 vai para a janela 3
    assert "80.0%" in linhas[2] and "0.100" in linhas[2] and "| 100 |" in linhas[2]
