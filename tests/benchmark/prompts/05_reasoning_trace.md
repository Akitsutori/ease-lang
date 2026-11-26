# Arena 05: Reasoning Trace (No Code Execution)

Read the 5 Ease snippets below using ONLY the Ease language rules in `llms.txt`. Do NOT execute any code.
Output ONLY a valid JSON object with exact keys `"q1"`, `"q2"`, `"q3"`, `"q4"`, `"q5"` and no markdown fences or commentary.

## Snippet 1 (`q1`: number)
What numeric value is printed?
```ease
xs = list 10, 20, 30
ys = xs with .2 = xs.2 + 5
say ys.2 + xs.2
```

## Snippet 2 (`q2`: text string)
What text value is printed?
```ease
def Prio = "urgent" | "routine" | "low"
def Ticket
    name = ""
    prio = Prio
end
tickets = list as Ticket with
    name = "Bram", prio = "low"
    name = "Aria", prio = "urgent"
    name = "Cora", prio = "routine"
end
ordered = sort tickets by it.prio
say ordered.1.name
```

## Snippet 3 (`q3`: number)
What numeric value is printed?
```ease
nums = list 1, 2, 3, 4
res = loop (nums where it > 1) -> it * 10 + it.index
say sum res
```

## Snippet 4 (`q4`: `"valid"` or `"type_error"`)
Does this Ease program pass static checking (`"valid"`) or fail with a `TypeError` (`"type_error"`)?
```ease
x = any (range 1 5) where it > 3
say 1
```

## Snippet 5 (`q5`: `"pass"` or `"runtime_error"`)
Does this snippet pass cleanly (`"pass"`) or fail at runtime with division by zero (`"runtime_error"`)?
```ease
xs = list 2, 0
check any xs where (10 / it) is 5
    say "pass"
end
```
