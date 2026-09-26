"""The inputs scripts/fixtures.py runs through the Python reference."""

# The state and questions of laya-coreml's examples/, which its validation uses.
EMAIL = {
    "from": "user@example.com",
    "subject": "Duplicate charge on invoice #4411",
    "body": "We were billed twice for March. Please refund the duplicate today or we will cancel our plan.",
}
EXAMPLE_QUESTIONS = [
    {
        "type": "choice",
        "instructions": "Which department should handle this email?",
        "criteria": {
            "billing": "invoices, payments, refunds",
            "technical": "bugs, outages, system errors",
            "sales": "pricing, new contracts",
            "other": "everything else",
        },
    },
    {
        "type": "score",
        "instructions": "How urgent is this request?",
        "criteria": ["not urgent", "soon", "critical deadline or blocking issue"],
    },
    {"type": "noul", "instructions": "Does the customer ask for money back?"},
]


def workload(count=3, long=False):
    state = dict(EMAIL)
    if long:
        state["body"] = "The customer reports duplicate billing and requests a refund today. " * 200
    return state, {f"q{i}": EXAMPLE_QUESTIONS[i % 3] for i in range(count)}


def validation_cases():
    """laya-coreml's benchmarks/cases.py parity_cases: what validation.json measured."""
    state, questions = workload()
    cases = [("email", state, questions)]
    messages = {
        "en": "I was charged twice for invoice 4411, please refund it today.",
        "zh": "发票4411被重复扣款，请今天退款。",
        "de": "Ich wurde zweimal für Rechnung 4411 belastet, bitte erstatten Sie den Betrag.",
        "fr": "J'ai été facturé deux fois pour la facture 4411, remboursez-moi s'il vous plaît.",
        "es": "Me cobraron dos veces la factura 4411, por favor devuélvanme el dinero.",
        "hi": "मुझसे इनवॉइस 4411 के लिए दो बार शुल्क लिया गया, कृपया पैसे वापस करें।",
        "ja": "請求書4411で二重に請求されました。返金してください。",
        "ru": "С меня дважды списали деньги по счёту 4411, верните деньги.",
    }
    for lang, message in messages.items():
        cases.append((lang, {"message": message}, questions))
    cases += [
        ("empty_state", "", questions),
        ("long", *workload(3, long=True)),
        ("conversation", [{"role": "user", "content": messages["en"]}], questions),
        ("mask_literals", "[MASK] <mask> hello [MASK] <mask>", questions),
        ("many_questions", *workload(20)),
        (
            "structured",
            state,
            {
                "choice": {
                    "type": "choice",
                    "instructions": {"task": "choose department"},
                    "criteria": {"billing": {"description": "refunds"}, "other": False},
                },
                "score": {"type": "score", "instructions": "Urgency?", "criteria": [{"level": "low"}, "high"]},
                "noul": {"type": "noul", "instructions": "Refund?", "criteria": {"true": {"reason": "money back"}}},
            },
        ),
        (
            "twenty_options",
            state,
            {
                "choice": {
                    "type": "choice",
                    "instructions": "Which department handles billing?",
                    "criteria": ["billing"] + [f"department_{i}" for i in range(19)],
                }
            },
        ),
    ]
    return cases


# ideacheck's own questions (configs/rubrics), in the wire form Go sends them:
# options and noul criteria as Go marshals a map, keys sorted.
IDEACHECK_QUESTIONS = {
    "idea_type": {
        "type": "choice",
        "instructions": "What kind of idea is this?",
        "criteria": {
            "business": "A product, service, or startup intended to make money",
            "content": "A blog, newsletter, video series, podcast, course, or similar media",
            "creative": "A game, story, artwork, music, or other creative work",
            "other": "None of the above",
            "research": "An investigation, experiment, paper, or study",
            "side_project": "A tool or app built mainly for personal use, learning, or fun",
        },
    },
    "problem_acuity": {
        "type": "score",
        "instructions": "How acute is the problem this idea addresses?",
        "criteria": [
            "No real problem, or a mild inconvenience people ignore",
            "A real annoyance with acceptable existing workarounds",
            "A painful problem people solve with clumsy hacks or manual work",
            "A blocking problem with no workable existing solution",
        ],
    },
    "sisp": {
        "type": "noul",
        "instructions": "The idea starts from a technology or trend and works backward to find a problem, rather than starting from a problem people already have.",
        "criteria": {
            "false": "A specific problem or customer comes first, and the technology is how it gets solved.",
            "true": "The technology or trend comes first, and the problem or customer is vague, deferred, or picked to fit it.",
        },
    },
    "audience_specificity": {
        "type": "score",
        "instructions": "How specific and reachable is the target audience?",
        "criteria": [
            "Everyone, or undefined",
            "A broad demographic",
            "A specific role, community, or situation",
            "A specific role in a specific context with a clear place to find them",
        ],
    },
    "has_problem": {
        "type": "noul",
        "instructions": "The description in `idea` states a specific problem that specific people have today.",
    },
}

IDEAS = {
    "meetups": {"idea": {"text": "An app that helps groups of friends pick a time and place to meet up."}},
    "blockchain": {
        "idea": {
            "text": "We want to use blockchain and large language models together. We are looking for an industry where putting AI agents on-chain would be useful, maybe supply chain or healthcare."
        }
    },
    "dentists": {
        "idea": {
            "audience": "Independent dental practices in the US with 1-5 chairs",
            "problem": "Front desks lose about a fifth of bookings to no-shows and spend hours a week calling patients to confirm.",
            "text": "An SMS assistant that confirms appointments, fills cancellations from a waitlist, and syncs with Dentrix.",
        }
    },
}


def ideacheck_cases():
    return [(name, state, IDEACHECK_QUESTIONS) for name, state in IDEAS.items()]


def texts():
    return [
        "Hello, world!",
        "I was charged twice for invoice 4411, please refund it today.",
        "don't won't I'm we've they'll she'd it's O'Neil's 'quoted'",
        "UPPER lower MiXeD 12345 3.14159 1,000,000 $5.99 50%",
        "a  b   c    d          e",
        "                                   thirty-five spaces",
        "trailing spaces   ",
        "   leading spaces",
        "tabs\there\t\tand\nnewlines\n\n\nand\r\nwindows",
        " \n mixed \t whitespace  nbsp em-space　ideographic",
        "发票4411被重复扣款，请今天退款。",
        "मुझसे इनवॉइस 4411 के लिए दो बार शुल्क लिया गया, कृपया पैसे वापस करें।",
        "請求書4411で二重に請求されました。返金してください。",
        "С меня дважды списали деньги по счёту 4411, верните деньги.",
        "مرحبا بالعالم، هذه فكرة تطبيق",
        "emoji 🚀🔥👩‍💻 and flags 🇺🇸 ok",
        "café vs café, Å vs Å, ﬁ ligature, Å angstrom, Ω ohm, 益 compat",
        "[CLS] special [SEP] tokens [MASK] in [PAD] text [UNK]",
        "hello [MASK] world  [MASK]",
        "<mask> <bos> <eos> <pad> <start_of_turn>user",
        "contact |||EMAIL_ADDRESS||| or |||IP_ADDRESS||| now",
        '{"idea": {"problem": "Dentists lose 20% of bookings to no-shows", "text": "A to-do app for dentists"}}',
        "choice question: Which kind of idea is this?",
        " level 0: No real problem, or a mild inconvenience people ignore",
        " false: no, the statement does not hold",
        "noul question: The idea starts from a technology or trend and works backward to find a problem, rather than starting from a problem people already have.",
        "!!!??? ... --- *** ((( ))) \"\" '' `` ~~ @@ ## && || \\\\ //",
        "x's!'s 's 'S 'll 'LL",
        "",
        " ",
        "  ",
        "\n",
        "zero​width‍space﻿bom ﻿\r\n",
        "control\u0001chars\u007fdel\u0085nel",
        "Ünïcödé ßtraße Ωmega ǅ ⅻ ① ²³ ٣٤",
    ]
