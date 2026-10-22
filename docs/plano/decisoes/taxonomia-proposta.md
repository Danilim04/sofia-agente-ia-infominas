# Taxonomia proposta (E5.1, preparação) — categorias = áreas do catálogo do Infodesk

Estado: **proposta, ainda não promovida**. `agente-ia/config/taxonomia.yaml` continua o
placeholder (`placeholder: true`); a proposta está em
`agente-ia/config/taxonomia.proposta.yaml` (mesmo formato, carrega em
`carregar_taxonomia`). A E5.1 só fecha quando a proposta virar `taxonomia.yaml` sem
`placeholder` (ver "O que muda ao promover").

## Decisões do dono do produto já tomadas (2026-10-22)

1. **Mesa única:** a Sofia abre todo chamado na mesa **"Abertura de Ticket"** (entrada
   única, triagem humana). A classificação define prioridade e item/área, não a mesa.
2. **Categorias = o que está no Infodesk:** seguir de forma explícita o catálogo do
   Infodesk, **sem ajustar o processo**: as categorias da Sofia são exatamente as **17
   áreas do catálogo** da mesa "Abertura de Ticket" (Microsoft 365, Google Workspace,
   Firewall, Redes, Servidores, Backup, Segurança, Computadores, Impressoras, Telefonia,
   Softwares, Monitoramento, Cloud, Banco de Dados, Certificados Digitais, Projetos,
   Consultoria), mais `indefinida`. Nada é juntado, separado ou criado. Isto substitui a
   primeira versão da proposta (9 categorias "visão do cliente", mergeada no PR #35).

O resto (departamento, prioridade sem `Media`, item de catálogo) continua como **proposta
marcada `DECISÃO PENDENTE`** no YAML; as recomendações estão em Q2–Q4. Sobre a aprovação: o dono respondeu "está aprovado" à proposta (2026-10-22, mensagem à sessão que
a preparou), pedindo para seguir explicitamente o Infodesk e "apenas seguir as categorias". Isso decide
mesa única e Q1. **Não** há resposta explícita sobre Q2–Q4; elas seguem como recomendação,
aguardando confirmação, e as marcas `DECISÃO PENDENTE` do YAML ficam até lá.

## O que os dados dizem (fatos do inventário de 2026-10-21)

- **"Abertura de Ticket" só tem as prioridades `Alta` e `Baixa`.** Não há `Media` nem
  `Crítica` (as outras mesas têm).
- Seu catálogo é o **mestre**: 399 itens em 17 áreas (Microsoft 365 75, Redes 49, Firewall
  35, Servidores 35, Monitoramento 23, Computadores 21, Segurança 20, Backup 18, Softwares
  17, Telefonia 16, Google Workspace 15, Cloud 15, Impressoras 14, Banco de Dados 13,
  Certificados Digitais 13, Projetos 10, Consultoria 10).
- O ticket só aceita `catalogItemId` (item); **área não é campo** do ticket nem do formato
  da taxonomia. O item é opcional (`requireTicketFields` desligado, `10` §8 linha 2).
- **O catálogo não tem área financeira nem comercial** (nenhum item de cobrança, boleto ou
  plano). Esses assuntos caem em `indefinida` (ver Q6).
- Departamentos de chat existentes: `Técnicos`, `Administrativo / Financeiro`, `Comercial`
  (sem vínculo com mesa; a escolha é nossa, `10` §2).
- 4 nomes de item se repetem no catálogo ("Implantação Microsoft 365", "Conceder permissão
  em pasta", "Segmentação de rede", "Assessment de segurança"): não são usados.

## Categorias propostas

Cada categoria é o nome da área em snake_case sem acento; `tests/test_taxonomia_proposta.py`
confere que o conjunto é exatamente o das 17 áreas da fixture (`infodesk_referencia.json`).
Todas com `mesa: Abertura de Ticket`, `catalogo` omitido (Q4) e **sem** prioridade mínima (Q3).
Os `exemplos` vêm dos casos reais onde existem; nas áreas sem caso real nos 32 são **nomes de
item do próprio catálogo**. O departamento é proposta (Q2).

| Categoria (área do catálogo) | Itens do catálogo (exemplos) | Casos reais | Departamento proposto | Por quê |
|---|---|---|---|---|
| `microsoft_365` (Microsoft 365) | Não envia e-mails, Outlook não abre, Teams não abre, OneDrive não sincroniza, MFA não funciona | 1 | Técnicos | suporte técnico |
| `google_workspace` (Google Workspace) | Gmail não envia, Conta Google bloqueada, Drive indisponível | 0 | Técnicos | suporte técnico |
| `firewall` (Firewall) | VPN não conecta, Site bloqueado indevidamente, Túnel site-to-site indisponível | 1 | Técnicos | suporte técnico |
| `redes` (Redes) | Sem internet, Internet lenta, Rede lenta, Wi-Fi indisponível, Perda de pacotes | 11 | Técnicos | suporte técnico |
| `servidores` (Servidores) | Servidor indisponível, Compartilhamento inacessível, AD indisponível | 2 | Técnicos | suporte técnico |
| `backup` (Backup) | Backup falhou, Restaurar arquivo, Repositório cheio | 0 | Técnicos | suporte técnico |
| `seguranca` (Segurança) | Malware detectado, Phishing recebido, EDR bloqueou aplicação | 0 | Técnicos | suporte técnico |
| `computadores` (Computadores) | Computador não liga, Computador lento, Tela azul, Windows não inicia | 2 | Técnicos | suporte técnico |
| `impressoras` (Impressoras) | Não imprime, Impressora offline, Scanner não funciona | 1 | Técnicos | suporte técnico |
| `telefonia` (Telefonia) | Ramal não registra, Chamada cai, Não recebe ligação | 0 | Técnicos | suporte técnico |
| `softwares` (Softwares) | ERP não abre, Sistema lento, Erro de licença | 0 | Técnicos | suporte técnico |
| `monitoramento` (Monitoramento) | Alerta de disco crítico, Alerta de host offline, Alerta de CPU alta | 0 | Técnicos | suporte técnico |
| `cloud` (Cloud) | VM Azure indisponível, DNS Cloudflare incorreto | 0 | Técnicos | suporte técnico |
| `banco_de_dados` (Banco de Dados) | Banco indisponível, Erro de conexão, Banco lento | 0 | Técnicos | suporte técnico |
| `certificados_digitais` (Certificados Digitais) | Certificado não reconhecido, Token não reconhecido, Erro em assinatura digital | 0 | Técnicos | suporte técnico |
| `projetos` (Projetos) | Cabeamento estruturado, Implantação Wi-Fi, Documentação de ambiente | 1 | Comercial | projeto novo é proposta/orçamento; único departamento de vendas |
| `consultoria` (Consultoria) | Assessment de TI, Auditoria de ambiente, Plano Diretor de TI | 0 | Comercial | serviço novo, idem |
| `indefinida` | vago, áudio/imagem sem texto, assunto sem área (boleto, cobrança, preço) | 3 (vago, áudio, imagem); os outros 8 cenários sem categoria são pedido de atendente, status, fora de escopo, injeção, senha de terceiro e acesso/senha | Técnicos (= padrão proposto) | sem assunto; a triagem humana é a mesma mesa |

Casos reais: dos 32 cenários, 21 caem numa área (redes 11, servidores 2, computadores 2,
impressoras 1, projetos 1, e-mail 1 e VPN 1, que nos cenários ainda aparecem como `indefinida`
porque o placeholder não tem a categoria, e 2 de financeiro, sem área), e 11 não têm categoria (áudio, foto,
vago, fora de escopo, injeção, pedido de atendente, pessoa específica, senha de terceiro, status
com e sem número, acesso/senha de usuário).

`mesa_triagem` = `Abertura de Ticket` (mesma mesa). `departamento_humano_padrao` = `Técnicos`
(proposta, Q2).

### Prioridades (bloco `prioridades` da proposta)

| Nome da Sofia | Nome em "Abertura de Ticket" | Situação |
|---|---|---|
| `baixa` | `Baixa` | existe |
| `media` | — (`null`: não enviar `priorityId`) | **proposta (Q3)**: a mesa não tem `Media` |
| `alta` | `Alta` | existe |
| `urgente` | `Alta` | **proposta (Q3)**: a mesa não tem `Crítica`; a maior que existe |

## Perguntas e recomendações

### Q1 — As categorias estão certas? **DECIDIDA**

Decisão do dono (2026-10-22): as categorias são as áreas do catálogo do Infodesk, exatamente.
A versão anterior (9 categorias agrupadas pelo que o cliente escreve) foi descartada.
Consequências aceitas: áreas sem nenhum caso real nos 32 (Google Workspace, Backup, Segurança,
Telefonia, Softwares, Monitoramento, Cloud, Banco de Dados, Certificados Digitais, Consultoria) viram
categoria com exemplos tirados dos nomes de item; `Monitoramento` é uma área de alertas de máquina e
raramente chega por WhatsApp de cliente, mas está no catálogo e fica; e o que não tem área no
catálogo vai para `indefinida` (Q6).

### Q2 — Departamento do handoff: por categoria e o padrão

Duas respostas: (a) a coluna "Departamento proposto" acima (todas as áreas técnicas para
`Técnicos`; `projetos` e `consultoria` para `Comercial`); (b) o `departamento_humano_padrao`
(usado quando a categoria é `indefinida`).

| Opção para o padrão | Efeito |
|---|---|
| **`Técnicos` (recomendada)** | é o que `10` §8 já propunha; quase todas as áreas são técnicas |
| `Administrativo / Financeiro` ou `Comercial` | só faz sentido se a Infominas quiser a recepção por lá |

Perguntas que o dono precisa fechar junto: (1) cada um dos três departamentos tem atendentes
ativos hoje? (`GET /users`, `departmentIds`; `10` §4: sem `userId` a conversa entra na fila
`PENDING` e quem é notificado são os atendentes do departamento); (2) o horário humano (P7,
E5.5) — nenhum dos 32 cenários depende dele.

Como boleto e cobrança não têm área, eles vão ao departamento **padrão**, não ao
`Administrativo / Financeiro` (Q6).

### Q3 — Prioridades: o que fazer sem `Media` e `Crítica`

| Sub-decisão | Opções | Recomendação |
|---|---|---|
| `media` (também é a prioridade padrão quando o JEV tem pouca confiança, `02`) | (i) não enviar `priorityId`: o ticket nasce na prioridade padrão da mesa e a triagem escolhe; (ii) mapear para `Baixa`; (iii) mapear para `Alta`; (iv) pedir à Infominas que crie `Media` na mesa | **(i)**: não atribui à força uma urgência que a mesa não tem; é o que o gateway já faz com `null` |
| `urgente` | `Alta` (proposta) ou não enviar | `Alta`: a maior que existe |
| Piso por categoria (`prioridade_minima`) | nenhum; ou `alta` em áreas "tudo parado" | **nenhum**: nada nos dados dá motivo explícito; `redes` cobre "caiu" e "lenta" e um piso `alta` subiria todo chamado de lentidão |

Nenhum dos 32 cenários afirma prioridade. Afeta o `priorityId` do ticket e os cenários da
raiz que testam piso (`piso-de-prioridade-vence-o-jev`), que hoje usam o piso do placeholder.

### Q4 — Item de catálogo (`catalogItemId`): omitir, usar item ou usar área?

A decisão de 22/10 diz que a classificação define "item/área". A categoria **é** a área; o
formato da taxonomia só tem `catalogo` (nome de **um item**) e o ticket só aceita item.

| Opção | Efeito |
|---|---|
| **A. Omitir `catalogo` (recomendada)** | a triagem humana escolhe o item; a área fica na classificação gravada em `conversas.classificacao_json` e na descrição do ticket; é o que a proposta faz |
| B. Item só onde a categoria equivale a um item | nenhuma área tem um item só; exigiria categorias por item, contra a decisão de seguir as áreas |
| C. Criar o campo `area` no formato | só serviria para texto/auditoria (o ticket não tem área); exige mudança em `taxonomia.py`/`taxonomia.go` |

### Q5 — Cadastro de telefone desconhecido e CPF (E5.3, não é da taxonomia)

Dois cenários assumem o fluxo antigo (telefone sem cadastro → pede CPF). A decisão de 22/10 de
cadastrar o solicitante via `POST /requesters` (E5.3) muda esse fluxo; "telefone cadastrado
dispensa CPF?" segue em `07` N3. Aqui só ficam parados.

Destrava: `real-nao-identificado-informa-cpf`, `real-cpf-invalido`.

### Q6 — Boleto, cobrança e preço: sem área no catálogo (NOVA)

Efeito direto de seguir só o catálogo: 2 dos 32 cenários reais (`real-cobranca-financeiro`,
`real-financeiro-segunda-via`) são de financeiro, e o catálogo não tem nada financeiro. Com as 17
áreas eles viram `indefinida`, e o caminho depende da intenção:

- **reclamação** (cobrança duplicada, `real-cobranca-financeiro`): a Sofia esclarece uma vez e, se continuar
  `indefinida`, transfere ao departamento padrão (`Técnicos`, Q2), **não** ao `Administrativo / Financeiro`;
- **pedido de informação** (boleto, 2ª via, preço; intenção `outro`/`duvida_informacao`,
  `real-financeiro-segunda-via`): não há pergunta de esclarecimento (só reclamação a tem); a categoria fica
  `indefinida`, sem ação do roteador, e a transferência só acontece se o LLM chamar
  `transferir_para_atendente`. O roteamento por categoria fora de reclamação (E3.10) não tem o que fixar.

| Opção | Efeito |
|---|---|
| **A. Seguir só o catálogo (decisão atual)** | boleto/cobrança → `indefinida` → atendente do departamento padrão; os dois cenários precisam ser reescritos (o esperado muda: esclarece e transfere) |
| B. Pedir à Infominas uma área financeira (e opcionalmente comercial) no catálogo | a categoria passa a existir sem inventar nada fora do Infodesk; os dois cenários sobem como estão |
| C. Departamento padrão = `Administrativo / Financeiro` | corrige o destino dos pedidos financeiros, mas leva para lá todo o resto de `indefinida` |

Recomendação: **A agora; B se o volume financeiro justificar** (medir com `analise-infodesk`).
`projetos`/`consultoria` ficam com `Comercial` para o pedido de orçamento (`real-assunto-comercial-planos`).

## Cenários reais: o que foi promovido e o que cada decisão destrava

**11 na raiz** (`evals/conversa/`, rodam no CI): os que não dependem de categoria, válidos na
taxonomia em uso e na proposta: `real-cliente-manda-audio`, `real-cliente-manda-foto`,
`real-fora-de-escopo`, `real-injecao-de-prompt`, `real-mensagem-vaga`, `real-pede-atendente`,
`real-pede-pessoa-especifica`, `real-pede-senha-de-terceiro`, `real-status-com-numero`,
`real-status-sem-numero` e `real-senha-acesso-usuario` (a conta pode ser do Microsoft 365, do
Google ou do AD, sem área inequívoca: `indefinida`, e esclarecer e transferir é o esperado).

**21 em `pendentes/`**, já remapeados para as áreas (`test_taxonomia_proposta.py` executa 19
deles contra a proposta e todos passam). Não podem subir antes da promoção porque o runner
valida a categoria contra `taxonomia.yaml`, onde `redes` ainda não existe.

| Destrava | Cenários |
|---|---|
| **Promoção da proposta** (confirmar Q2–Q4; depois `git mv`) | `real-internet-caiu-empresa`, `real-internet-lenta`, `real-internet-oscila`, `real-dois-links-fora-urgente`, `real-formulario-do-site`, `real-cita-protocolo-nao-abre-novo`, `real-cliente-irritado`, `real-cliente-impaciente-sem-transferir`, `real-vago-depois-descreve` (`redes`); `real-computador-lento`, `real-pergunta-nao-confirma` (`computadores`); `real-impressora-nao-imprime` (`impressoras`); `real-servidor-desligando`, `real-muda-descricao-antes-de-confirmar` (`servidores`); `real-assunto-comercial-planos` (`projetos`) |
| **Promoção + reescrita** (hoje descrevem o comportamento sem categoria; com a área o esperado muda: abre chamado) | `real-email-nao-envia` (`microsoft_365`), `real-vpn-nao-conecta` (`firewall`) |
| **Q5** (E5.3) | `real-nao-identificado-informa-cpf`, `real-cpf-invalido` |
| **Q6** (sem área financeira; não rodam contra a proposta) | `real-cobranca-financeiro`, `real-financeiro-segunda-via` |

Cada arquivo em `pendentes/` traz uma linha `# Destrava: …` com a mesma informação.

## O que muda ao promover a proposta a `taxonomia.yaml` (fechamento da E5.1)

Não faz parte desta preparação; é o custo de trocar as categorias:

1. **38 cenários da raiz** usam categorias do placeholder (`sem_conexao`, `lentidao`,
   `cobranca`…), assim como 16 arquivos de `tests/` (`test_classificacao_*`, `test_graph*`,
   `test_server*`, `test_e2e_infodesk`, `test_infodesk_tools`, `test_tool_policy`,
   `test_acao_sem_ferramenta`, `test_app`, `test_observability_*`, `fakes.py`). Remapear no mesmo PR.
2. `evals/classificacao/sintetico.jsonl` foi escrito para o placeholder (provedor de internet);
   `python -m evals.classificacao --fonte sintetico --jev` (critério da E5.1) só faz sentido depois
   de reescrevê-lo para estas áreas (zona da E2.4). `tests/test_taxonomia_proposta.py` guarda o
   destino de cada categoria antiga.
3. `scripts/dev/taxonomia.dev.yaml` (mock) acompanha as categorias novas.
4. Três testes de `tests/test_taxonomia_proposta.py` ficam vermelhos **de propósito** quando a
   proposta virar `taxonomia.yaml` (`test_proposta_carrega_e_continua_placeholder`,
   `test_nao_substitui_a_taxonomia_em_uso`, `test_promovidos_sao_validos_tambem_na_taxonomia_em_uso`):
   são removidos ou adaptados, não "consertados".
5. O gateway Go lê o mesmo arquivo (`backend/internal/taxonomia`, `Parse`); a forma da proposta
   (`media: null`, `urgente` e `alta` apontando para `Alta`) foi conferida com esse `Parse`
   (verificação manual; nenhum teste Go carrega a proposta).
6. `agente-ia/evals/README.md` ainda manda promover tudo de `pendentes/` com `git mv`; apontar para a
   seção "Casos reais" de `evals/conversa/README.md`.
7. Rodar o gateway contra o Infodesk real: o boot resolve mesa, prioridade e departamento por nome
   exato; `tests/fixtures/infodesk_referencia.json` já prova que os nomes da proposta existem.

## Como conferir

```bash
cd agente-ia
pytest tests/test_taxonomia_proposta.py -v     # áreas, nomes contra o Infodesk, decisões marcadas, 30 cenários
pytest -m evals_pr evals/                      # CI de hoje, com os 11 promovidos
```
