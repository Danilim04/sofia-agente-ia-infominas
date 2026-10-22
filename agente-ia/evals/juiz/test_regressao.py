"""Regressão (> 2 pts) e agregação do juiz — números calculados à mão, sem rede."""

from __future__ import annotations

import json
from pathlib import Path

import pytest

from evals.juiz import regressao as reg
from evals.juiz.parser import NotaCriterio
from evals.juiz.relatorio import RelatorioJuiz, ResultadoJuiz

pytestmark = pytest.mark.evals_pr


# ------------------------------------------------------------------ agregação do juiz
def _r(id_: str, **notas: int) -> ResultadoJuiz:
    return ResultadoJuiz(
        id_, f"cenário {id_}", {k: NotaCriterio(v, "ok") for k, v in notas.items()}
    )


def test_metricas_planas_calculadas_a_mao():
    rel = RelatorioJuiz(
        (
            _r("a", persona=5, exatidao=3),
            _r("b", persona=4),
            ResultadoJuiz("c", "falhou", problema="execucao", detalhe="LLM fora"),
            ResultadoJuiz("d", "juiz torto", problema="juiz_invalido", detalhe="sem JSON"),
        )
    )
    m = rel.metricas_planas()
    # persona: (5+4)/2 = 4.5 → 90 · exatidao: 3 → 60 · geral: (100+60+80)/3 = 80
    assert m["juiz.persona.media"] == pytest.approx(90.0)
    assert m["juiz.exatidao.media"] == pytest.approx(60.0)
    assert m["juiz.geral.media"] == pytest.approx(80.0)
    # notas ≥ 4: 5 e 4 de 3 notas → 2/3
    assert m["juiz.aprovacao"] == pytest.approx(66.67, abs=0.01)
    assert "juiz.nao_inventa.media" not in m  # critério que ninguém avaliou não vira 0
    assert rel.contagens() == {
        "cenarios": 4,
        "julgados": 2,
        "erros_execucao": 1,
        "juiz_invalido": 1,
    }


def test_cenario_sem_nota_nao_entra_na_media_e_aparece_no_relatorio():
    rel = RelatorioJuiz(
        (
            _r("a", persona=5),
            ResultadoJuiz("c", "caiu", problema="execucao", detalhe="X no turno 2"),
        )
    )
    assert rel.metricas_planas()["juiz.persona.media"] == pytest.approx(100.0)
    md = rel.para_markdown()
    assert "execucao: X no turno 2" in md and "| a — cenário a |" in md


def test_relatorio_lista_notas_abaixo_de_4_com_justificativa():
    rel = RelatorioJuiz((ResultadoJuiz("a", "d", {"persona": NotaCriterio(2, "prometeu prazo")}),))
    assert "**a** `persona` = 2/5: prometeu prazo" in rel.para_markdown()


# --------------------------------------------------------------- regressão (> tolerância)
def test_juiz_queda_acima_de_2_pontos_e_regressao_e_exatamente_2_nao():
    base = {"juiz.persona.media": 90.0, "juiz.exatidao.media": 80.0, "juiz.geral.media": 85.0}
    atuais = {"juiz.persona.media": 87.5, "juiz.exatidao.media": 78.0, "juiz.geral.media": 86.0}
    achadas = reg.comparar("juiz", "juiz/noturno", atuais, base, 2.0, reg._sentido_juiz)
    # persona: 90 − 87.5 = 2.5 > 2 → regressão · exatidão: exatos 2.0 → não · geral subiu → não
    assert [(r.metrica, r.piora) for r in achadas] == [("juiz.persona.media", pytest.approx(2.5))]


def test_juiz_melhora_nao_e_regressao_mas_metrica_que_sumiu_e_alerta():
    base = {"juiz.persona.media": 90.0, "juiz.nao_inventa.media": 90.0}
    achadas = reg.comparar("juiz", "k", {"juiz.persona.media": 99.0}, base, 2.0, reg._sentido_juiz)
    # persona subiu → nada; nao_inventa estava no baseline e não saiu → "sumiu" (rodada quebrada ≠ sem regressão)
    assert [(r.metrica, r.sumiu) for r in achadas] == [("juiz.nao_inventa.media", True)]
    assert "**sumiu**" in achadas[0].linha()
    # rodada em que todo cenário falhou: nenhuma métrica → todas as do baseline sumiram
    assert len(reg.comparar("juiz", "k", {}, base, 2.0, reg._sentido_juiz)) == 2


def test_metrica_nao_bloqueante_ausente_continua_ignorada():
    base = {"categoria.acuracia": 80.0, "latencia.p95_ms": 100.0}
    assert reg.comparar("classificação", "k", {}, base, 2.0, reg._sentido_classificacao) == []


def test_baseline_de_outra_versao_do_prompt_nao_e_comparado():
    assert reg.versao_incompativel({"versao_prompt": "juiz-v2"}, {"versao_prompt": "juiz-v1"})
    assert reg.versao_incompativel({"versao_prompt": "juiz-v1"}, {"versao_prompt": "juiz-v1"}) == ""
    assert reg.versao_incompativel({"versao_prompt": "juiz-v1"}, {"versao_prompt": None}) == ""


def test_classificacao_f1_e_acuracia_caem_ece_sobe():
    base = {
        "categoria.macro_f1": 91.3,
        "categoria.acuracia_no_limiar": 96.0,
        "categoria.ece": 5.0,
        "categoria.acuracia": 80.0,  # não é métrica bloqueante
        "latencia.p95_ms": 100.0,  # nem esta
    }
    atuais = {
        "categoria.macro_f1": 88.0,  # −3.3 → regressão
        "categoria.acuracia_no_limiar": 95.0,  # −1.0 → dentro da tolerância
        "categoria.ece": 8.0,  # +3.0 → regressão (menor é melhor)
        "categoria.acuracia": 10.0,
        "latencia.p95_ms": 9999.0,
    }
    achadas = reg.comparar(
        "classificação", "sintetico/jev", atuais, base, 2.0, reg._sentido_classificacao
    )
    assert sorted(r.metrica for r in achadas) == ["categoria.ece", "categoria.macro_f1"]
    por_metrica = {r.metrica: r for r in achadas}
    assert por_metrica["categoria.macro_f1"].piora == pytest.approx(3.3)
    assert por_metrica["categoria.ece"].piora == pytest.approx(3.0)


def test_baseline_vazio_nunca_acusa_regressao():
    relatorio = {"chave": "sintetico/jev", "metricas": {"categoria.macro_f1": 1.0}}
    assert reg.regressoes_classificacao(relatorio, {"metricas": {}}) == []
    assert reg.regressoes_classificacao(relatorio, {}) == []
    assert reg.regressoes_juiz({"metricas": {"juiz.geral.media": 1.0}}, {"metricas": {}}) == []


def test_tolerancia_vem_do_baseline_ou_do_argumento():
    relatorio = {"chave": "sintetico/jev", "metricas": {"categoria.macro_f1": 88.0}}
    baseline = {
        "tolerancia_pontos": 5.0,
        "metricas": {"sintetico/jev": {"categoria.macro_f1": 91.3}},
    }
    assert reg.regressoes_classificacao(relatorio, baseline) == []  # −3.3 < 5
    assert len(reg.regressoes_classificacao(relatorio, baseline, tolerancia=1.0)) == 1


def test_comentario_tem_marcador_tabela_e_link():
    r = reg.Regressao("juiz", "juiz/noturno", "juiz.persona.media", 90.0, 87.5, 2.5)
    texto = reg.montar_comentario([r], "https://github.com/o/r/actions/runs/1")
    assert texto.startswith(reg.MARCADOR)
    assert "| juiz | `juiz/noturno` | `juiz.persona.media` | 90.00 | 87.50 | +2.50 |" in texto
    assert "https://github.com/o/r/actions/runs/1" in texto
    assert "Não bloqueia merge" in texto


# ------------------------------------------------------------------------- CLI ponta a ponta
def _escrever(caminho: Path, dado: object) -> Path:
    caminho.write_text(json.dumps(dado), encoding="utf-8")
    return caminho


def test_cli_com_regressao_escreve_comentario_e_output(tmp_path: Path):
    base_cls = _escrever(
        tmp_path / "base-cls.json",
        {"tolerancia_pontos": 2.0, "metricas": {"sintetico/jev": {"categoria.macro_f1": 91.3}}},
    )
    base_juiz = _escrever(
        tmp_path / "base-juiz.json",
        {"metricas": {"juiz/noturno": {"juiz.persona.media": 90.0, "juiz.geral.media": 85.0}}},
    )
    cls_rel = _escrever(
        tmp_path / "cls.json", {"chave": "sintetico/jev", "metricas": {"categoria.macro_f1": 91.0}}
    )
    juiz_rel = _escrever(
        tmp_path / "juiz.json",
        {
            "chave": "juiz/noturno",
            "metricas": {"juiz.persona.media": 80.0, "juiz.geral.media": 85.0},
        },
    )
    saida, gh = tmp_path / "comentario.md", tmp_path / "gh_output"
    linhas: list[str] = []
    codigo = reg.main(
        [
            "--classificacao", str(cls_rel),
            "--juiz", str(juiz_rel),
            "--baseline-classificacao", str(base_cls),
            "--baseline-juiz", str(base_juiz),
            "--saida", str(saida),
            "--github-output", str(gh),
        ],
        saida_texto=linhas.append,
    )  # fmt: skip
    assert codigo == 0
    assert gh.read_text(encoding="utf-8") == "regressao=true\n"
    comentario = saida.read_text(encoding="utf-8")
    assert (
        "juiz.persona.media" in comentario and "categoria.macro_f1" not in comentario
    )  # só 1 piorou
    assert any("REGRESSÃO em 1" in linha for linha in linhas)


def test_cli_sem_regressao_ou_sem_relatorio_nao_gera_comentario(tmp_path: Path):
    gh, saida = tmp_path / "gh", tmp_path / "c.md"
    linhas: list[str] = []
    codigo = reg.main(
        [
            "--classificacao", str(tmp_path / "nao-existe.json"),
            "--juiz", str(tmp_path / "tambem-nao.json"),
            "--saida", str(saida),
            "--github-output", str(gh),
        ],
        saida_texto=linhas.append,
    )  # fmt: skip
    assert codigo == 0 and not saida.exists()
    assert gh.read_text(encoding="utf-8") == "regressao=false\n"
    assert sum("não existe" in linha for linha in linhas) == 2


def test_cli_json_ilegivel_e_erro(tmp_path: Path):
    ruim = tmp_path / "ruim.json"
    ruim.write_text("{quebrado", encoding="utf-8")
    assert (
        reg.main(
            ["--juiz", str(ruim), "--github-output", str(tmp_path / "gh")],
            saida_texto=lambda _: None,
        )
        == 2
    )


def test_baselines_versionados_estao_validos():
    for caminho in (reg.BASELINE_CLASSIFICACAO, reg.BASELINE_JUIZ):
        dado = json.loads(caminho.read_text(encoding="utf-8"))
        assert dado["tolerancia_pontos"] == 2.0 and isinstance(dado["metricas"], dict)


def test_cli_rodada_quebrada_vira_alerta_e_nota_de_cenarios_parciais(tmp_path: Path):
    base_juiz = _escrever(
        tmp_path / "base.json",
        {"metricas": {"juiz/noturno": {"juiz.geral.media": 85.0}}, "versao_prompt": "juiz-v1"},
    )
    juiz_rel = _escrever(
        tmp_path / "juiz.json",
        {
            "chave": "juiz/noturno",
            "versao_prompt": "juiz-v1",
            "contagens": {"cenarios": 26, "julgados": 0, "erros_execucao": 26, "juiz_invalido": 0},
            "metricas": {},
        },
    )
    saida, gh = tmp_path / "c.md", tmp_path / "gh"
    linhas: list[str] = []
    codigo = reg.main(
        ["--juiz", str(juiz_rel), "--baseline-juiz", str(base_juiz), "--saida", str(saida), "--github-output", str(gh)],
        saida_texto=linhas.append,
    )  # fmt: skip
    assert codigo == 0 and gh.read_text(encoding="utf-8") == "regressao=true\n"
    comentario = saida.read_text(encoding="utf-8")
    assert "**sumiu**" in comentario and "só 0 de 26 cenários receberam nota" in comentario
    assert any("sumiu da rodada" in linha for linha in linhas)


def test_cli_baseline_de_outra_versao_do_prompt_ignora_a_comparacao(tmp_path: Path):
    base = _escrever(
        tmp_path / "b.json",
        {"versao_prompt": "juiz-v1", "metricas": {"juiz/noturno": {"juiz.geral.media": 90.0}}},
    )
    rel = _escrever(
        tmp_path / "j.json",
        {
            "chave": "juiz/noturno",
            "versao_prompt": "juiz-v2",
            "metricas": {"juiz.geral.media": 10.0},
        },
    )
    gh, linhas = tmp_path / "gh", list[str]()
    codigo = reg.main(
        ["--juiz", str(rel), "--baseline-juiz", str(base), "--github-output", str(gh)],
        saida_texto=linhas.append,
    )
    assert codigo == 0 and gh.read_text(encoding="utf-8") == "regressao=false\n"
    assert any("regrave o baseline" in linha for linha in linhas)
