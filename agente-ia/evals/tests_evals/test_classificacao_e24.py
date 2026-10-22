"""E2.4: runner sobre o `classificar()` real — modo offline, janelas, métricas novas e modos reais.

Sem rede: o modo offline usa `JEVSimulado`/`FallbackSimulado`; os testes `jev` e `llm_real`
só rodam com chave (excluídos por default no `addopts` do pyproject).
"""

from __future__ import annotations

import json
import os
import re
import socket
from pathlib import Path
from typing import Any

import pytest

from evals import classificacao as cls
from evals.anonimizar import Anonimizador
from evals.classificacao import __main__ as cli
from evals.classificacao.adaptador import ErroModo, criar_classificador
from evals.formatos import (
    INTENCOES,
    CasoClassificacao,
    ErroFormato,
    Esperado,
    carregar_taxonomia,
    validar_caso,
)
from evals.relatorio import gerar_relatorio

TAXONOMIA = carregar_taxonomia()
EXEMPLO_OFFLINE = (
    Path(__file__).resolve().parent.parent / "exemplos" / "relatorio-offline-exemplo.md"
)


def _casos() -> list[CasoClassificacao]:
    return cls.carregar_dataset(cls.FONTES_ARQUIVO["sintetico"], TAXONOMIA)


def _caso(esperado: Esperado, intencao_estado: str = "Cliente: oi") -> CasoClassificacao:
    return CasoClassificacao(
        id="c", fonte="sintetico", estado=(intencao_estado,), esperado=esperado
    )


# --------------------------------------------------------------------- dataset
def test_dataset_v1_tem_mais_de_150_casos_e_cobre_o_que_a_e24_pede():
    casos = _casos()
    assert len(casos) >= 150
    assert sum(1 for c in casos if len(c.estado) > 6) >= 10, "históricos longos (janela 10 ≠ 6)"
    assert sum(1 for c in casos if len(c.estado) > 10) >= 3, "históricos acima da janela 10"
    assert sum(1 for c in casos if 3 <= len(c.estado) <= 6) >= 10, "históricos médios"
    assert sum(1 for c in casos if c.esperado.malicioso_min) >= 5
    assert sum(1 for c in casos if c.esperado.fora_de_escopo_min) >= 4
    assert sum(1 for c in casos if c.esperado.malicioso_max is not None) >= 30, (
        "negativos de injeção"
    )
    assert sum(1 for c in casos if c.esperado.fora_de_escopo_max is not None) >= 30
    assert {c.esperado.intencao for c in casos} == set(INTENCOES), "toda intenção do 02 aparece"
    graus = {
        n
        for c in casos
        for n in (c.esperado.frustracao_min, c.esperado.frustracao_max)
        if n is not None
    }
    assert graus >= {0, 1, 2, 3}, "frustração nos 4 graus"


def test_dataset_sintetico_sem_dado_pessoal():
    """O anonimizador (CPF, CNPJ, telefone, e-mail, CEP, placa, URL, nº longo) não acha nada."""
    anon = Anonimizador()
    for caso in _casos():
        for mensagem in caso.estado:
            assert anon.texto(mensagem) == mensagem, f"{caso.id}: dado pessoal em {mensagem!r}"
        assert caso.rotulador is None
    assert sum(anon.contagem.values()) == 0
    # Defesa extra: nenhuma sequência de 6+ dígitos nem arroba nos textos do dataset.
    bruto = cls.FONTES_ARQUIVO["sintetico"].read_text(encoding="utf-8")
    assert not re.search(r"\d{6,}", bruto)
    assert "@" not in bruto


def test_dataset_ids_unicos_e_formato_valido():
    ids = [c.id for c in _casos()]
    assert len(ids) == len(set(ids))
    with pytest.raises(ErroFormato, match="não pode ser maior"):
        validar_caso(
            {
                "id": "x",
                "fonte": "sintetico",
                "estado": ["Cliente: oi"],
                "esperado": {
                    "intencao": "outro",
                    "categoria": "indefinida",
                    "malicioso_min": 0.9,
                    "malicioso_max": 0.2,
                },
            }
        )


# ---------------------------------------------------------------- avaliar_caso
def test_avaliar_noul_binario_com_confianca_2p_menos_1():
    positivo = _caso(Esperado("outro", "indefinida", malicioso_min=0.8))
    r = cls.avaliar_caso(positivo, {"malicioso": 0.9}, 0.0).perguntas["malicioso"]
    assert (r.esperado, r.previsto, r.acerto) == ("sim", "sim", True)
    assert r.confianca == pytest.approx(0.8)  # |2·0.9 − 1|
    perdeu = cls.avaliar_caso(positivo, {"malicioso": 0.3}, 0.0).perguntas["malicioso"]
    assert (perdeu.previsto, perdeu.acerto) == ("nao", False)
    assert perdeu.confianca == pytest.approx(0.4)
    negativo = _caso(Esperado("outro", "indefinida", malicioso_max=0.5))
    ok = cls.avaliar_caso(negativo, {"malicioso": 0.2}, 0.0).perguntas["malicioso"]
    assert (ok.esperado, ok.previsto, ok.acerto) == ("nao", "nao", True)
    falso_positivo = cls.avaliar_caso(negativo, {"malicioso": 0.7}, 0.0).perguntas["malicioso"]
    assert (falso_positivo.previsto, falso_positivo.acerto) == ("sim", False)
    # sem min/max no caso, ou sem o Noul na resposta: não entra
    assert (
        "malicioso"
        not in cls.avaliar_caso(
            _caso(Esperado("outro", "indefinida")), {"malicioso": 0.9}, 0
        ).perguntas
    )
    assert "malicioso" not in cls.avaliar_caso(positivo, {}, 0).perguntas


def test_avaliar_frustracao_dentro_da_faixa():
    caso = _caso(Esperado("outro", "indefinida", frustracao_min=2, frustracao_max=3))
    for nivel, esperado in ((1, False), (2, True), (3, True)):
        par = cls.avaliar_caso(caso, {"frustracao": nivel}, 0).perguntas["frustracao"]
        assert par.acerto is esperado and par.esperado == "2..3" and par.exata is False
    so_teto = _caso(Esperado("outro", "indefinida", frustracao_max=1))
    assert (
        cls.avaliar_caso(so_teto, {"frustracao": 0}, 0).perguntas["frustracao"].esperado == "0..1"
    )
    assert (
        "frustracao"
        not in cls.avaliar_caso(
            _caso(Esperado("outro", "indefinida")), {"frustracao": 3}, 0
        ).perguntas
    )
    assert "frustracao" not in cls.avaliar_caso(caso, {}, 0).perguntas  # classificador sem JEV


def test_categoria_final_so_nos_casos_de_reclamacao():
    resposta: dict[str, Any] = {
        "intencao": "outro",
        "categoria": "segunda_via",
        "final": {"categoria": "indefinida", "confianca": 0.0},
    }
    duvida = _caso(Esperado("duvida_informacao", "segunda_via"))
    assert "categoria_final" not in cls.avaliar_caso(duvida, resposta, 0).perguntas
    reclamacao = _caso(Esperado("reclamacao_problema", "sem_conexao"))
    par = cls.avaliar_caso(
        reclamacao, {**resposta, "final": {"categoria": "sem_conexao", "confianca": 0.9}}, 0
    ).perguntas["categoria_final"]
    assert par.acerto is True and par.confianca == pytest.approx(0.9)


def test_rodar_prefere_a_latencia_informada_pelo_classificador():
    def classificador(estado: list[str]) -> dict[str, Any]:
        return {"intencao": "outro", "categoria": "indefinida", "latencia_ms": 123.0}

    [r] = cls.rodar([_caso(Esperado("outro", "indefinida"))], classificador)
    assert r.latencia_ms == 123.0


# ---------------------------------------------------------------- modo offline
@pytest.fixture
def sem_rede(monkeypatch: pytest.MonkeyPatch) -> None:
    """Qualquer conexão fora de loopback (e qualquer DNS) derruba o teste.

    Loopback fica liberado porque o event loop do Windows usa um `socketpair` interno.
    """
    conectar_original = socket.socket.connect

    def conectar(self: socket.socket, endereco: Any) -> None:
        host = endereco[0] if isinstance(endereco, tuple) else endereco
        if host not in ("127.0.0.1", "::1", "localhost"):
            raise AssertionError(f"o modo offline não pode abrir conexão de rede ({host})")
        conectar_original(self, endereco)

    def dns(*args: object, **kwargs: object) -> None:
        raise AssertionError("o modo offline não pode resolver nomes (rede)")

    monkeypatch.setattr(socket.socket, "connect", conectar)
    monkeypatch.setattr(socket, "getaddrinfo", dns)
    monkeypatch.setenv("TYPESAFE_API_KEY", "")
    monkeypatch.setenv("OPENROUTER_API_KEY", "")


def _rodar_offline(janela: int | None = None, limite: int = 0) -> list[Any]:
    casos = _casos()[:limite] if limite else _casos()
    classificador, _ = cls.obter_classificador("offline", janela)
    try:
        return cls.rodar(casos, classificador)
    finally:
        classificador.fechar()  # type: ignore[attr-defined]


def test_offline_roda_sem_rede_e_e_deterministico(sem_rede: None):
    primeira = gerar_relatorio(_rodar_offline(), cls.LIMIARES_PADRAO).metricas_planas()
    segunda = gerar_relatorio(_rodar_offline(), cls.LIMIARES_PADRAO).metricas_planas()
    assert primeira == segunda
    assert primeira["categoria.cobertura_no_limiar"] > 0  # o JEV simulado decide parte dos casos
    assert 0 < primeira["categoria.acuracia"] < 100  # não é gabarito vazando nem nulo
    assert (
        primeira["latencia.p50_ms"] > 0
        and primeira["latencia.p95_ms"] >= primeira["latencia.p50_ms"]
    )


def test_offline_nao_enxerga_o_esperado(sem_rede: None):
    """Duas rodadas com os mesmos estados e `esperado` embaralhado dão as mesmas previsões."""
    casos = _casos()[:60]
    trocados = [
        CasoClassificacao(
            id=c.id, fonte=c.fonte, estado=c.estado, esperado=Esperado("outro", "indefinida")
        )
        for c in casos
    ]
    classificador, _ = cls.obter_classificador("offline")
    try:
        a = [r.perguntas["categoria"].previsto for r in cls.rodar(casos, classificador)]
        b = [r.perguntas["categoria"].previsto for r in cls.rodar(trocados, classificador)]
    finally:
        classificador.fechar()  # type: ignore[attr-defined]
    assert a == b


def test_offline_usa_o_classificar_real_com_estado_mascarado(sem_rede: None):
    classificador, descricao = criar_classificador("offline")
    try:
        resposta = classificador(
            ["Cliente: minha internet caiu, meu cpf 529.982.247-25 e tel 31 99999-1234"]
        )
    finally:
        classificador.fechar()
    estado_enviado, perguntas = classificador._jev._interno.chamadas[-1]  # type: ignore[union-attr]
    assert "<CPF>" in estado_enviado and "<TEL>" in estado_enviado
    assert "529.982" not in estado_enviado and "99999" not in estado_enviado
    assert set(perguntas) == {
        "intencao",
        "categoria",
        "prioridade",
        "frustracao",
        "malicioso",
        "fora_de_escopo",
    }
    assert resposta["extras"]["fallback_chamado"] in (True, False)
    assert resposta["final"]["categoria"] in TAXONOMIA.categorias
    assert descricao.simulado is True and descricao.modo == "offline"


@pytest.mark.parametrize(("janela", "linhas_esperadas"), [(1, 1), (3, 3), (6, 6), (10, 10)])
def test_janela_limita_as_mensagens_enviadas_ao_jev(
    sem_rede: None, janela: int, linhas_esperadas: int
):
    longo = next(c for c in _casos() if len(c.estado) >= 10)
    classificador, descricao = criar_classificador("offline", janela)
    try:
        classificador(list(longo.estado))
    finally:
        classificador.fechar()
    estado_enviado = classificador._jev._interno.chamadas[-1][0]  # type: ignore[union-attr]
    assert len(estado_enviado.splitlines()) == linhas_esperadas
    assert estado_enviado.splitlines()[-1].startswith("Mensagem atual:")
    assert descricao.janela == janela


def test_janela_maior_enxerga_o_assunto_que_a_menor_perdeu(sem_rede: None):
    """No histórico longo cujo assunto só aparece no começo, a janela 10 acerta onde a 1 não acerta."""
    longos = [c for c in _casos() if len(c.estado) >= 9]
    acertos: dict[int, int] = {}
    for janela in (1, 10):
        classificador, _ = cls.obter_classificador("offline", janela)
        try:
            resultados = cls.rodar(longos, classificador)
        finally:
            classificador.fechar()  # type: ignore[attr-defined]
        acertos[janela] = sum(r.perguntas["categoria"].acerto for r in resultados)
    assert acertos[10] > acertos[1]


def test_offline_recusa_chave_ausente_nos_modos_reais(sem_rede: None):
    with pytest.raises(ErroModo, match="P8"):
        criar_classificador("jev")
    with pytest.raises(ErroModo, match="OPENROUTER_API_KEY"):
        criar_classificador("llm")


def test_jev_com_chave_sem_openrouter_roda_sem_fallback_e_avisa(monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("TYPESAFE_API_KEY", "chave-falsa")
    monkeypatch.setenv("OPENROUTER_API_KEY", "")
    classificador, descricao = criar_classificador("jev", 3)
    classificador.fechar()  # nada chamou a rede: o cliente HTTP só nasce na primeira pergunta
    assert "JEVTypeSafe" in descricao.jev and descricao.fallback == "nenhum"
    assert descricao.janela == 3 and not descricao.simulado
    assert any("OPENROUTER_API_KEY" in a for a in descricao.avisos)


# ------------------------------------------------------------------------- CLI
def _cli(tmp_path: Path, *args: str) -> tuple[int, list[str]]:
    linhas: list[str] = []
    codigo = cli.main(
        [*args, "--baseline", str(tmp_path / "b.json"), "--relatorio", str(tmp_path / "r.md")],
        saida_texto=linhas.append,
    )
    return codigo, linhas


def test_cli_padrao_e_offline_e_marca_o_relatorio(sem_rede: None, tmp_path: Path):
    codigo, saida = _cli(tmp_path, "--fonte", "sintetico", "--max", "30")
    assert codigo == 0
    texto = (tmp_path / "r.md").read_text(encoding="utf-8")
    assert texto.splitlines()[2].startswith("> **Exemplo offline com fakes, não é medição real")
    assert "latência**: simulada" in texto
    assert any("não é medição real" in linha.lower() for linha in saida)


def test_cli_sem_chave_recusa_jev_e_llm(sem_rede: None, tmp_path: Path):
    for flag, chave in (("--jev", "TYPESAFE_API_KEY"), ("--llm", "OPENROUTER_API_KEY")):
        codigo, saida = _cli(tmp_path, "--fonte", "sintetico", flag, "--max", "3")
        assert codigo == 2 and chave in saida[-1]
        assert not (tmp_path / "r.md").exists()


def test_cli_janelas_gera_tabela_comparativa(sem_rede: None, tmp_path: Path):
    codigo, saida = _cli(tmp_path, "--fonte", "sintetico", "--janelas", "--max", "80")
    assert codigo == 0
    texto = (tmp_path / "r.md").read_text(encoding="utf-8")
    assert "## Comparação de janelas de contexto (C-JEV1)" in texto
    for janela in (1, 3, 6, 10):
        assert re.search(rf"^\| \**{janela}\** \|", texto, re.MULTILINE), (
            f"linha da janela {janela}"
        )
    dado = json.loads((tmp_path / "r.json").read_text(encoding="utf-8"))
    assert set(dado["janelas"]) == {"1", "3", "6", "10"}
    assert dado["chave"] == "sintetico/offline"  # detalhe na janela de referência (6)
    medias = [dado["janelas"][j]["latencia.p50_ms"] for j in ("1", "3", "6", "10")]
    assert all(m > 0 for m in medias)
    assert sum(1 for linha in saida if linha.startswith("janela ")) == 4


def test_cli_janela_fora_da_referencia_nao_usa_o_baseline_padrao(sem_rede: None, tmp_path: Path):
    codigo, _ = _cli(tmp_path, "--fonte", "sintetico", "--janela", "3", "--max", "20")
    assert codigo == 0
    assert (
        json.loads((tmp_path / "r.json").read_text(encoding="utf-8"))["chave"]
        == "sintetico/offline@j3"
    )


def test_cli_argumentos_invalidos(sem_rede: None, tmp_path: Path):
    assert _cli(tmp_path, "--fonte", "sintetico", "--janelas", "0,3")[0] == 2
    assert _cli(tmp_path, "--fonte", "sintetico", "--janelas", "a,b")[0] == 2
    assert _cli(tmp_path, "--fonte", "sintetico", "--janela", "0")[0] == 2
    assert _cli(tmp_path, "--fonte", "sintetico", "--nulo", "--janelas")[0] == 2


def test_exemplo_offline_commitado_esta_em_dia(sem_rede: None, tmp_path: Path):
    """O exemplo do repositório é o relatório offline atual (sem a linha `gerado_em`).

    Mudou o dataset, o simulador ou as métricas? Regenere com:
    `python -m evals.classificacao --fonte sintetico --offline --janelas --relatorio evals/exemplos/relatorio-offline-exemplo.md`
    (e apague o `.json` ao lado).
    """
    codigo, _ = _cli(tmp_path, "--fonte", "sintetico", "--offline", "--janelas")
    assert codigo == 0

    def sem_data(texto: str) -> str:
        return "\n".join(linha for linha in texto.splitlines() if "**gerado_em**" not in linha)

    atual = sem_data((tmp_path / "r.md").read_text(encoding="utf-8"))
    commitado = sem_data(EXEMPLO_OFFLINE.read_text(encoding="utf-8"))
    assert atual == commitado
    assert (
        commitado.splitlines()[2]
        .lower()
        .startswith("> **exemplo offline com fakes, não é medição real")
    )


# --------------------------------------------------- modos reais (precisam de chave)
def _metricas(modo: str, limite: int = 0) -> dict[str, float]:
    casos = _casos()[:limite] if limite else _casos()
    classificador, _ = cls.obter_classificador(modo)
    try:
        resultados = cls.rodar(casos, classificador)
    finally:
        classificador.fechar()  # type: ignore[attr-defined]
    return gerar_relatorio(resultados, cls.LIMIARES_PADRAO).metricas_planas()


@pytest.mark.jev
@pytest.mark.skipif(not os.environ.get("TYPESAFE_API_KEY"), reason="TYPESAFE_API_KEY ausente (P8)")
def test_jev_real_intencao_cumpre_os_criterios_do_02():
    """acurácia@limiar ≥ 0.95 e ECE ≤ 0.08 (docs/plano/02, critério inicial a revisar com o P9)."""
    m = _metricas("jev")
    assert m["intencao.acuracia_no_limiar"] >= 95.0
    assert m["intencao.ece"] <= 8.0


@pytest.mark.jev
@pytest.mark.skipif(not os.environ.get("TYPESAFE_API_KEY"), reason="TYPESAFE_API_KEY ausente (P8)")
@pytest.mark.xfail(
    TAXONOMIA.placeholder,
    reason="taxonomia placeholder (P1): categoria é informativa até o export do Infodesk",
    strict=False,
)
def test_jev_real_categoria_cumpre_os_criterios_do_02():
    """macro-F1 ≥ 0.85, acurácia@limiar ≥ 0.95, ECE ≤ 0.08 (xfail enquanto `placeholder: true`)."""
    m = _metricas("jev")
    assert m["categoria.macro_f1"] >= 85.0
    assert m["categoria.acuracia_no_limiar"] >= 95.0
    assert m["categoria.ece"] <= 8.0


@pytest.mark.llm_real
@pytest.mark.skipif(not os.environ.get("OPENROUTER_API_KEY"), reason="OPENROUTER_API_KEY ausente")
def test_fallback_llm_real_responde_so_com_opcoes_fechadas():
    classificador, _ = cls.obter_classificador("llm")
    try:
        resultados = cls.rodar(_casos()[:25], classificador)
    finally:
        classificador.fechar()  # type: ignore[attr-defined]
    for r in resultados:
        assert r.perguntas["intencao"].previsto in INTENCOES
        assert r.perguntas["categoria"].previsto in TAXONOMIA.categorias
        assert r.perguntas["categoria"].confianca == 0.0  # sem JEV não há confiança de JEV
    acuracia = sum(r.perguntas["intencao"].acerto for r in resultados) / len(resultados)
    assert acuracia >= 0.5, "um LLM com opções fechadas não pode errar metade das intenções"


# ------------------------------------- caminhos do adaptador fora do offline feliz
def _montar(jev_script=(), fb_padrao=None, *, com_jev=True):
    """ClassificadorReal sobre os fakes compartilhados (JEV scriptado / fallback scriptado)."""
    from evals.classificacao.adaptador import ClassificadorReal, GravadorFallback, GravadorJEV
    from src.classificacao.fallback_llm import ResultadoFallback
    from tests.fakes import FakeFallbackLLM, FakeJEV, servico_fake

    gj = GravadorJEV(FakeJEV(list(jev_script))) if com_jev else None
    gf = GravadorFallback(
        FakeFallbackLLM(padrao=fb_padrao or ResultadoFallback("outro", "indefinida"))
    )
    servico = servico_fake(jev=gj, fallback=gf)  # type: ignore[arg-type]
    return ClassificadorReal(servico, gj, gf), gf


def test_jev_que_falha_cai_no_fallback_com_confianca_zero():
    from src.classificacao.fallback_llm import ResultadoFallback
    from src.classificacao.jev import JEVIndisponivel

    classificador, _ = _montar(
        [JEVIndisponivel("timeout")], ResultadoFallback("reclamacao_problema", "sem_conexao")
    )
    try:
        r = classificador(["Cliente: minha internet caiu"])
    finally:
        classificador.fechar()
    assert r["extras"]["jev_indisponivel"] is True and r["extras"]["fallback_chamado"] is True
    assert (r["intencao"], r["categoria"]) == ("reclamacao_problema", "sem_conexao")
    assert r["confiancas"]["intencao"] == 0.0 and r["confiancas"]["categoria"] == 0.0
    assert "frustracao" not in r and "malicioso" not in r  # sem JEV não há Score nem Noul


def test_modo_llm_sem_jev_usa_o_fallback_e_nao_marca_jev_indisponivel():
    from src.classificacao.fallback_llm import ResultadoFallback

    classificador, _ = _montar(
        fb_padrao=ResultadoFallback("duvida_informacao", "duvida_planos"), com_jev=False
    )
    try:
        r = classificador(["Cliente: quanto custa o plano de 500 mega?"])
    finally:
        classificador.fechar()
    assert (r["intencao"], r["categoria"]) == ("duvida_informacao", "duvida_planos")
    assert r["confiancas"]["categoria"] == 0.0
    assert "jev_indisponivel" not in r["extras"]


def test_fallback_com_ok_false_nao_vira_previsao():
    from src.classificacao.fallback_llm import ResultadoFallback
    from src.classificacao.jev import JEVIndisponivel

    classificador, _ = _montar(
        [JEVIndisponivel("x")], ResultadoFallback("outro", "indefinida", ok=False)
    )
    try:
        r = classificador(["Cliente: minha internet caiu"])
    finally:
        classificador.fechar()
    assert r["extras"]["fallback_chamado"] is True
    assert (r["intencao"], r["categoria"]) == (r["final"]["intencao"], r["final"]["categoria"])


def test_prioridade_bruta_do_jev_difere_da_final_com_piso():
    """JEV diz `baixa`; o piso `alta` da taxonomia de sem_conexao vence na final."""
    from tests.fakes import resp_jev

    classificador, _ = _montar([resp_jev(prioridade=(0, 0.9))])
    try:
        r = classificador(["Cliente: minha internet caiu"])
    finally:
        classificador.fechar()
    assert r["prioridade"] == "baixa" and r["final"]["prioridade"] == "alta"
    par = cls.avaliar_caso(
        _caso(Esperado("reclamacao_problema", "sem_conexao", "alta")), r, 0
    ).perguntas
    assert par["prioridade"].acerto is False and par["prioridade_final"].acerto is True
    assert par["prioridade"].confianca == pytest.approx(0.9)


def test_fallback_real_e_montado_com_o_llm_barato(monkeypatch: pytest.MonkeyPatch):
    from types import SimpleNamespace

    import src.agent.llm as llm_mod

    class LLMFalso:
        def with_structured_output(self, schema: object) -> LLMFalso:
            return self

        async def ainvoke(self, mensagens: object) -> SimpleNamespace:
            return SimpleNamespace(intencao="reclamacao_problema", categoria="lentidao")

    monkeypatch.setattr(llm_mod, "get_classifier_llm", LLMFalso)
    monkeypatch.setenv("TYPESAFE_API_KEY", "")
    monkeypatch.setenv("OPENROUTER_API_KEY", "chave-falsa")
    classificador, descricao = criar_classificador("llm")
    try:
        r = classificador(["Cliente: a net ta lenta"])
    finally:
        classificador.fechar()
    assert "OpenRouter" in descricao.fallback and descricao.jev == "nenhum"
    assert (r["intencao"], r["categoria"]) == ("reclamacao_problema", "lentidao")
    assert r["extras"]["fallback_chamado"] is True


# --------------------------------------------- chave do baseline, JSON e avisos da CLI
def test_chave_do_baseline_segue_a_janela_efetiva(sem_rede: None, tmp_path: Path):
    """`--janelas 1,3` (sem o 6) detalha a janela 1: não pode gravar como baseline padrão."""
    assert _cli(tmp_path, "--fonte", "sintetico", "--janelas", "1,3", "--max", "20")[0] == 0
    assert json.loads((tmp_path / "r.json").read_text(encoding="utf-8"))["chave"].endswith("@j1")


def test_offline_ignora_variaveis_de_ambiente_do_jev(
    sem_rede: None, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
):
    monkeypatch.setenv("JEV_JANELA_MSGS", "2")
    monkeypatch.setenv("JEV_LIMIAR_CATEGORIA_ALTO", "0.99")
    codigo, _ = _cli(tmp_path, "--fonte", "sintetico", "--max", "40")
    assert codigo == 0
    dado = json.loads((tmp_path / "r.json").read_text(encoding="utf-8"))
    assert dado["chave"] == "sintetico/offline"  # janela 6, a de referência
    assert "janela de contexto**: 6 mensagens" in (tmp_path / "r.md").read_text(encoding="utf-8")
    assert os.environ["JEV_JANELA_MSGS"] == "2"  # a isolação restaura o ambiente


def test_json_do_relatorio_offline_diz_que_e_simulado(sem_rede: None, tmp_path: Path):
    assert _cli(tmp_path, "--fonte", "sintetico", "--max", "10")[0] == 0
    dado = json.loads((tmp_path / "r.json").read_text(encoding="utf-8"))
    assert dado["simulado"] is True and dado["modo"] == "offline"
    assert "não é medição real" in dado["banner"]


def test_cli_avisa_quando_o_jev_nao_respondeu(
    sem_rede: None, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
):
    from evals.relatorio import ParPrevisao, ResultadoCaso

    def falso(modo: str, janela: int | None, casos: Any) -> tuple[list[ResultadoCaso], Any]:
        par = ParPrevisao("outro", "outro", 0.0, True)
        return (
            [
                ResultadoCaso("a", {"intencao": par}, 1.0, {"jev_indisponivel": True}),
                ResultadoCaso("b", {"intencao": par}, 1.0, {"jev_indisponivel": False}),
            ],
            None,
        )

    monkeypatch.setattr(cli, "_rodar_janela", falso)
    codigo, saida = _cli(tmp_path, "--fonte", "sintetico", "--nulo")
    assert codigo == 0
    assert any("JEV não respondeu em 1 de 2" in linha for linha in saida)
