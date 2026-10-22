"""Runner de classificação: dataset sintético válido, classificador injetável, CLI e baseline."""

from __future__ import annotations

import json
from itertools import count
from pathlib import Path

import pytest

from evals import classificacao as cls
from evals.classificacao import __main__ as cli
from evals.formatos import CasoClassificacao, ErroFormato, Esperado, carregar_taxonomia

TAXONOMIA = carregar_taxonomia()


def test_dataset_sintetico_valido_e_cobre_taxonomia():
    casos = cls.carregar_dataset(cls.FONTES_ARQUIVO["sintetico"], TAXONOMIA)
    assert len(casos) >= 165  # E2.4: ≥ 150 exigidos; o v0 já tinha 189 e a v1 passa de 230
    por_categoria = cls.contar_por_categoria(casos)
    assert set(por_categoria) == set(TAXONOMIA.categorias), (
        "toda categoria do placeholder tem casos"
    )
    for categoria, n in por_categoria.items():
        assert n >= 15, f"{categoria} tem só {n} casos (mínimo 15)"
    assert all(c.fonte == "sintetico" for c in casos)
    assert sum(1 for c in casos if c.esperado.prioridade_min) >= 60, "prioridade_min onde é óbvia"
    assert sum(1 for c in casos if len(c.estado) > 1) >= 15, (
        "casos com histórico (mais de uma mensagem)"
    )
    assert sum(1 for c in casos if c.notas and "fronteira" in c.notas.lower()) >= 20, (
        "casos de fronteira anotados"
    )
    assert sum(1 for c in casos if (c.esperado.frustracao_min or 0) >= 2) >= 5, (
        "casos irritados com piso de frustração"
    )
    assert any(c.esperado.malicioso_min for c in casos)
    assert any(c.esperado.fora_de_escopo_min for c in casos)


def test_carregar_dataset_aponta_linha_em_json_invalido(tmp_path: Path):
    arq = tmp_path / "x.jsonl"
    arq.write_text(
        '{"id": "a", "fonte": "sintetico", "estado": ["x"], "esperado": {"intencao": "outro", "categoria": "indefinida"}}\n{quebrado\n',
        encoding="utf-8",
    )
    with pytest.raises(ErroFormato) as exc:
        cls.carregar_dataset(arq)
    assert exc.value.campo == "x.jsonl:2"


def test_carregar_dataset_prefixa_arquivo_em_erro_de_formato(tmp_path: Path):
    arq = tmp_path / "y.jsonl"
    arq.write_text(
        '{"id": "a", "fonte": "sintetico", "estado": ["x"], "esperado": {"intencao": "outro", "categoria": "nada"}}\n',
        encoding="utf-8",
    )
    with pytest.raises(ErroFormato) as exc:
        cls.carregar_dataset(arq, TAXONOMIA)
    assert exc.value.campo == "y.jsonl casos[0].esperado.categoria"


def _caso(
    categoria="sem_conexao", intencao="reclamacao_problema", prioridade_min="alta"
) -> CasoClassificacao:
    return CasoClassificacao(
        id="c1",
        fonte="sintetico",
        estado=("Cliente: caiu",),
        esperado=Esperado(intencao, categoria, prioridade_min),
    )


def test_classificador_nulo_e_avaliacao():
    resposta = cls.classificador_nulo(["x"])
    assert resposta["categoria"] == "indefinida" and resposta["confiancas"]["categoria"] == 0.0
    r = cls.avaliar_caso(_caso(), resposta, 1.5)
    assert r.perguntas["categoria"].acerto is False
    assert r.perguntas["intencao"].acerto is False
    assert r.perguntas["prioridade"].acerto is False  # media < alta
    assert r.perguntas["prioridade"].exata is False
    assert r.latencia_ms == 1.5


def test_avaliar_caso_prioridade_ordinal_com_piso():
    resposta = {
        "intencao": "reclamacao_problema",
        "categoria": "sem_conexao",
        "prioridade": "urgente",
        "confiancas": {"categoria": 0.9},
    }
    r = cls.avaliar_caso(_caso(prioridade_min="alta"), resposta, 0)
    assert r.perguntas["prioridade"].acerto is True  # urgente ≥ alta
    assert r.perguntas["categoria"].acerto is True and r.perguntas["categoria"].confianca == 0.9
    sem_min = cls.avaliar_caso(_caso(prioridade_min=None), resposta, 0)
    assert "prioridade" not in sem_min.perguntas
    invalida = cls.avaliar_caso(_caso(), {**resposta, "prioridade": "zzz"}, 0)
    assert invalida.perguntas["prioridade"].acerto is False


def test_rodar_mede_latencia_com_relogio_injetado():
    ticks = count(start=0.0, step=0.010)  # cada leitura avança 10 ms
    resultados = cls.rodar([_caso(), _caso()], cls.classificador_nulo, relogio=lambda: next(ticks))
    assert [round(r.latencia_ms) for r in resultados] == [10, 10]


def test_obter_classificador_por_modo(monkeypatch: pytest.MonkeyPatch):
    """E2.2 existe: `jev`/`llm` não caem mais no nulo — sem chave, recusam com ErroModo."""
    from evals.classificacao.adaptador import ErroModo

    monkeypatch.setenv("TYPESAFE_API_KEY", "")
    monkeypatch.setenv("OPENROUTER_API_KEY", "")
    assert cls.obter_classificador("nulo")[1] == "nulo"
    classificador, nome = cls.obter_classificador("offline")
    assert nome == "offline"
    classificador.fechar()  # type: ignore[attr-defined]
    with pytest.raises(ErroModo, match="TYPESAFE_API_KEY"):
        cls.obter_classificador("jev")
    with pytest.raises(ErroModo, match="OPENROUTER_API_KEY"):
        cls.obter_classificador("llm")
    with pytest.raises(ValueError, match="modo"):
        cls.obter_classificador("mágico")


def test_cli_gera_relatorio_e_metricas(tmp_path: Path):
    md = tmp_path / "rel" / "r.md"
    linhas: list[str] = []
    codigo = cli.main(
        [
            "--fonte",
            "sintetico",
            "--offline",
            "--relatorio",
            str(md),
            "--baseline",
            str(tmp_path / "b.json"),
            "--max",
            "20",
        ],
        saida_texto=linhas.append,
    )
    assert codigo == 0
    assert md.exists()
    texto = md.read_text(encoding="utf-8")
    assert texto.startswith("# Evals de classificação — sintetico/offline")
    assert "placeholder" in texto
    assert "exemplo offline com fakes, não é medição real" in texto.lower()
    metricas = json.loads(md.with_suffix(".json").read_text(encoding="utf-8"))
    assert metricas["chave"] == "sintetico/offline"
    assert metricas["metricas"]["categoria.cobertura_no_limiar"] > 0.0  # o JEV simulado decide
    assert "latencia.p95_ms" in metricas["metricas"]
    assert any(linha.startswith("baseline sem métricas") for linha in linhas)


def test_cli_detecta_regressao_contra_baseline(tmp_path: Path):
    base = tmp_path / "b.json"
    base.write_text(
        json.dumps(
            {
                "versao": 1,
                "tolerancia_pontos": 2.0,
                "metricas": {"sintetico/offline": {"categoria.macro_f1": 99.0}},
            }
        ),
        encoding="utf-8",
    )
    linhas: list[str] = []
    codigo = cli.main(
        [
            "--fonte",
            "sintetico",
            "--relatorio",
            str(tmp_path / "r.md"),
            "--baseline",
            str(base),
            "--max",
            "10",
        ],
        saida_texto=linhas.append,
    )
    assert codigo == 1
    assert any("REGRESSÃO" in linha for linha in linhas)
    # tolerância gigante na linha de comando vence a do arquivo
    assert (
        cli.main(
            [
                "--fonte",
                "sintetico",
                "--relatorio",
                str(tmp_path / "r.md"),
                "--baseline",
                str(base),
                "--max",
                "10",
                "--tolerancia",
                "100",
            ],
            saida_texto=linhas.append,
        )
        == 0
    )


def test_cli_atualiza_baseline_com_justificativa(tmp_path: Path):
    base = tmp_path / "b.json"
    linhas: list[str] = []
    assert (
        cli.main(
            [
                "--fonte",
                "sintetico",
                "--relatorio",
                str(tmp_path / "r.md"),
                "--baseline",
                str(base),
                "--max",
                "5",
                "--atualizar-baseline",
            ],
            saida_texto=linhas.append,
        )
        == 2
    )
    assert "justificativa" in linhas[-1]
    assert (
        cli.main(
            [
                "--fonte",
                "sintetico",
                "--relatorio",
                str(tmp_path / "r.md"),
                "--baseline",
                str(base),
                "--max",
                "5",
                "--nulo",
                "--atualizar-baseline",
                "--justificativa",
                "teste",
            ],
            saida_texto=linhas.append,
        )
        == 0
    )
    dado = json.loads(base.read_text(encoding="utf-8"))
    assert dado["justificativa"] == "teste"
    assert "categoria.acuracia" in dado["metricas"]["sintetico/nulo"]
    assert dado["atualizado_em"]


def test_cli_recusa_baseline_no_modo_offline(tmp_path: Path):
    """O offline usa fakes: gravar isso como referência seria mentira (o baseline é de --jev/--llm)."""
    linhas: list[str] = []
    args = ["--fonte", "sintetico", "--offline", "--relatorio", str(tmp_path / "r.md")]
    args += ["--baseline", str(tmp_path / "b.json"), "--max", "5"]
    codigo = cli.main(
        [*args, "--atualizar-baseline", "--justificativa", "x"], saida_texto=linhas.append
    )
    assert codigo == 2
    assert "recusado" in linhas[-1]
    assert not (tmp_path / "b.json").exists()


def test_cli_dataset_ausente_ou_invalido(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    linhas: list[str] = []
    assert (
        cli.main(
            [
                "--fonte",
                "historico",
                "--relatorio",
                str(tmp_path / "r.md"),
                "--baseline",
                str(tmp_path / "b.json"),
            ],
            saida_texto=linhas.append,
        )
        == 2
    )
    assert "não existe" in linhas[-1]
    ruim = tmp_path / "ruim.jsonl"
    ruim.write_text('{"id": "a"}\n', encoding="utf-8")
    monkeypatch.setitem(cls.FONTES_ARQUIVO, "historico", ruim)
    assert (
        cli.main(
            [
                "--fonte",
                "historico",
                "--relatorio",
                str(tmp_path / "r.md"),
                "--baseline",
                str(tmp_path / "b.json"),
            ],
            saida_texto=linhas.append,
        )
        == 2
    )
    assert "dataset inválido" in linhas[-1]


def test_baseline_versionado_tem_estrutura():
    dado = cli.carregar_baseline(cli.BASELINE_PADRAO)
    assert dado["versao"] == 1 and isinstance(dado["metricas"], dict)
    assert dado["tolerancia_pontos"] == 2.0
