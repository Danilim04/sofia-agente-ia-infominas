"""Testes do próprio runner e das extensões do formato (E3.5).

Um eval que nunca reprova não prova nada: aqui o runner é obrigado a REPROVAR
cenários errados, e as extensões do YAML recusam entrada malformada com campo e motivo.
"""

from __future__ import annotations

import re
from pathlib import Path

import pytest

from evals.conversa import runner
from evals.conversa.extensoes import CenarioEval, carregar_cenario_eval
from evals.formatos import ErroFormato, carregar_taxonomia

pytestmark = pytest.mark.evals_pr

_JEV = (
    "{intencao: [reclamacao_problema, 0.93], categoria: [sem_conexao, 0.88], "
    "prioridade: [2, 0.7], frustracao: [1, 0.8], malicioso: 0.02, fora_de_escopo: 0.01}"
)


def _yaml(*, texto: str, espera: str, extra_turno: str = "", extra_cenario: str = "") -> str:
    return f"""\
id: conversa-900
descricao: cenário de teste do runner
{extra_cenario}
sessao: {{telefone: "+5531999990900", nome_contato: "Teste", contato: telefone}}
jev:
  - {_JEV}
turnos:
  - cliente: "minha internet caiu"
{extra_turno}
    llm:
      - texto: "{texto}"
    espera: {espera}
"""


def _cenario(
    tmp_path: Path,
    *,
    texto: str = "Poxa, que chato! Desde quando?",
    espera: str,
    extra_turno: str = "",
    extra_cenario: str = "",
) -> CenarioEval:
    arquivo = tmp_path / "c.yaml"
    arquivo.write_text(
        _yaml(texto=texto, espera=espera, extra_turno=extra_turno, extra_cenario=extra_cenario),
        encoding="utf-8",
    )
    return carregar_cenario_eval(arquivo, carregar_taxonomia().categorias)


def test_runner_aprova_cenario_correto(tmp_path: Path) -> None:
    c = _cenario(tmp_path, espera='{nao_chama: [abrir_chamado], contem: ["Desde quando"]}')
    assert runner.executar(c).passou


@pytest.mark.parametrize(
    ("espera", "trecho_da_falha"),
    [
        ('{nao_chama: [abrir_chamado], contem: ["prazo de 24 horas"]}', "contem"),
        ('{nao_chama: [abrir_chamado], nao_contem: ["Desde quando"]}', "nao_contem"),
        ("{transfere: true}", "transfere=True"),
        ("{chama: [abrir_chamado]}", "chama 'abrir_chamado'"),
        ("{nao_chama: [abrir_chamado], proposta_no_estado: true}", "proposta_no_estado"),
        ("{nao_chama: [abrir_chamado], bloqueado: true}", "bloqueado"),
        ("{nao_chama: [abrir_chamado], llm_chamadas: 0}", "llm_chamadas"),
        ("{nao_chama: [abrir_chamado], prompt_contem: [inexistente]}", "prompt_contem"),
        (
            "{nao_chama: [abrir_chamado], gateway_vezes: {/tools/infodesk/ticket/abrir: 1}}",
            "gateway_vezes",
        ),
        (
            "{nao_chama: [abrir_chamado], estado: {classificacao: {categoria: cobranca}}}",
            "estado.classificacao",
        ),
    ],
)
def test_runner_reprova_cenario_errado(tmp_path: Path, espera: str, trecho_da_falha: str) -> None:
    resultado = runner.executar(_cenario(tmp_path, espera=espera))
    assert not resultado.passou
    assert any(trecho_da_falha in f for f in resultado.falhas), resultado.falhas


def test_runner_reprova_resposta_fora_da_persona(tmp_path: Path) -> None:
    """A persona é conferida em TODA resposta final, sem o cenário pedir."""
    texto = "Prezado cliente, resolvemos em até 2 horas 😊😊"
    c = _cenario(tmp_path, texto=texto, espera="{nao_chama: [abrir_chamado]}")
    motivos = " | ".join(runner.executar(c).falhas)
    assert "mais de um emoji" in motivos
    assert "promete prazo" in motivos
    assert "linguagem corporativa" in motivos


@pytest.mark.parametrize(
    "texto",
    [
        "Oi! Tudo bem? 😊",
        "Pronto! Seu chamado é o 48213. A equipe assume daqui.",
        "Vou te passar para um atendente.",
    ],
)
def test_persona_aceita_respostas_boas(texto: str) -> None:
    assert runner.verificar_persona(texto, "t") == []


@pytest.mark.parametrize(
    "texto",
    [
        "Resolvemos em 24 horas.",
        "Um técnico passa em 10 minutos.",
        "Amanhã alguém te liga.",
        "Logo te retorno por aqui.",
        "Em instantes você tem a resposta.",
        "Fica pronto dentro do prazo combinado.",
        "Em breve uma pessoa fala com você.",
    ],
)
def test_persona_recusa_promessas_de_prazo(texto: str) -> None:
    assert any("promete prazo" in f for f in runner.verificar_persona(texto, "t")), texto


def test_tool_viu_confere_o_que_o_llm_recebeu_da_tool(tmp_path: Path) -> None:
    c = _cenario(
        tmp_path,
        espera='{nao_chama: [abrir_chamado], tool_viu_contem: ["algo que nenhuma tool devolveu"]}',
    )
    assert any("tool_viu_contem" in f for f in runner.executar(c).falhas)


def test_script_do_llm_com_passos_sobrando_reprova(tmp_path: Path) -> None:
    arquivo = tmp_path / "c.yaml"
    arquivo.write_text(
        _yaml(texto="Oi", espera="{nao_chama: [abrir_chamado]}").replace(
            '      - texto: "Oi"\n',
            '      - texto: "Oi"\n      - texto: "passo que o grafo nunca consome"\n',
        ),
        encoding="utf-8",
    )
    falhas = runner.executar(carregar_cenario_eval(arquivo)).falhas
    assert any("passos sobrando" in f for f in falhas), falhas


def test_persona_confere_bolha_a_bolha() -> None:
    assert runner.verificar_persona("Oi 😊\n---\nTudo bem 🙏", "t") == []  # 1 emoji por bolha
    assert runner.verificar_persona("Oi 😊🙏", "t")  # 2 na mesma bolha


def test_respostas_fixas_do_codigo_seguem_a_persona() -> None:
    from src.agent.prompts import (
        RESPOSTA_ACAO_SEM_FERRAMENTA,
        RESPOSTA_ERRO_INTERNO,
        RESPOSTA_OFF_TOPIC,
    )

    for fixa in (RESPOSTA_OFF_TOPIC, RESPOSTA_ERRO_INTERNO, RESPOSTA_ACAO_SEM_FERRAMENTA):
        assert runner.verificar_persona(fixa, "fixa") == []


@pytest.mark.parametrize(
    ("extra", "campo"),
    [
        ("    gateway: {ticket/consultar: erro-inexistente}", "gateway.ticket/consultar"),
        ("    gateway: {ticket/inexistente: ok}", "gateway.ticket/inexistente"),
        ("    rag: [{source: x.md}]", "rag[0].texto"),
        ("    avanca_minutos: -3", "avanca_minutos"),
    ],
)
def test_extensoes_malformadas_apontam_campo_e_motivo(
    tmp_path: Path, extra: str, campo: str
) -> None:
    with pytest.raises(ErroFormato, match=re.escape(campo)):
        _cenario(tmp_path, espera="{nao_chama: [abrir_chamado]}", extra_turno=extra)


def test_relogio_sem_fuso_e_recusado(tmp_path: Path) -> None:
    with pytest.raises(ErroFormato, match="relogio"):
        _cenario(
            tmp_path,
            espera="{nao_chama: [abrir_chamado]}",
            extra_cenario='relogio: "2026-10-21T10:00:00"',
        )


def test_espera_so_com_extensao_e_recusada(tmp_path: Path) -> None:
    with pytest.raises(ErroFormato, match="espera básica"):
        _cenario(tmp_path, espera="{llm_chamadas: 1}")


def test_paralelo_exige_ao_menos_duas_chamadas(tmp_path: Path) -> None:
    arquivo = tmp_path / "c.yaml"
    arquivo.write_text(
        _yaml(texto="Oi", espera="{nao_chama: [abrir_chamado]}").replace(
            "    llm:\n", "    llm:\n      - paralelo: [{tool: abrir_chamado}]\n"
        ),
        encoding="utf-8",
    )
    with pytest.raises(ErroFormato, match="paralelo"):
        carregar_cenario_eval(arquivo)


def test_resumo_e_relatorio_listam_todos_os_cenarios() -> None:
    cenarios = runner.carregar_cenarios()
    resultados = {c.id: runner.executar(c) for c in cenarios[:2]}
    assert runner.resumo(cenarios, resultados).count("conversa-") >= len(cenarios)
    md = runner.relatorio_markdown(cenarios, resultados)
    assert md.startswith("# Evals de conversa")
    assert "| id | comportamento |" in md
