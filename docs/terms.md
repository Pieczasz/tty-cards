# Terms of Service

> This file mirrors <https://www.tty.cards/terms/>. The body below is identical to
> `web/src/legal/terms.md`, which is what the web page imports. If the two ever
> disagree, the web page is the published version.

**Last updated: 19 September 2026**

By connecting to `tty.cards`, you agree to these terms. They are short on purpose.

---

## 1. What this is

tty.cards is a free, open-source hobby project: an SSH server that runs multiplayer
card games in your terminal. There is no company behind it, no subscription, no
payment, and no service level. It may be slow, restarted, broken, or switched off
without notice.

The source code is published under the **MIT Licence**
(<https://github.com/Pieczasz/tty-cards/blob/main/LICENSE>). That licence covers
the *code*, and lets you run your own copy. It is not a promise about this particular
server, which is what these terms are about.

---

## 2. Your account

Your SSH key is your account. The first time you connect, the username you use is
claimed against the SHA256 fingerprint of your public key; after that, the key is what
logs you in.

Keep your private key to yourself. Anyone holding it is your account, and we have no
way to tell the difference. There is no password to reset and no recovery process.

What we store, and why, is in the [Privacy Policy](https://www.tty.cards/privacy/).

---

## 3. Acceptable use

Play the games. Specifically, do **not**:

- **Manipulate ratings.** No running several accounts to feed yourself wins, no
  agreeing results with an opponent, no colluding at a table, no farming a
  cooperative or abandoned account, no deliberately losing to move someone's rating.
- **Abuse other players.** Harassment, threats, slurs and impersonation are not
  welcome, and a username counts as speech. Do not impersonate another player or the
  operator.
- **Attack the service.** No denial-of-service, no attempts to exhaust connections or
  seats, no probing for vulnerabilities on the live server, no exploiting a bug once
  you have noticed it, and no automated clients that play on your behalf or hold
  seats open.
- **Break the law** with it, or use it to help someone else do so.

Automation that is obviously friendly - a script that opens your terminal, a tool that
reads the public JSON API politely - is fine. A bot that plays ranked matches is not.

If you find a bug or a security hole, report it privately to the operator instead of
using it.

---

## 4. Enforcement

We may suspend or delete any account, end any session or match, and reset or remove
any rating, at our discretion and without notice, if we believe these terms have been
broken or the service is being harmed. Where it is practical and sensible, we will say
why.

We may also stop running the service entirely at any time, in which case the database
is deleted.

---

## 5. No warranty

The service is provided **"as is" and "as available", without warranty of any kind**,
express or implied, including but not limited to warranties of merchantability,
fitness for a particular purpose, availability, accuracy, or non-infringement. Matches
can be lost to a crash, a restart, or a network problem. Ratings can be wrong.
Nothing here is a guarantee that any of it works.

---

## 6. Limitation of liability

To the fullest extent permitted by law, the operator is not liable for any indirect,
incidental, special or consequential damages, for lost data, lost matches or lost
ratings, or for anything arising out of your use of, or inability to use, the service.
Where liability cannot be excluded by law, it is limited to the amount you paid to use
the service, which is zero.

Nothing in these terms limits liability for death or personal injury caused by
negligence, for fraud, or for anything else that cannot lawfully be excluded. If you
are a consumer in the EU, your mandatory statutory rights are unaffected by this
section.

---

## 7. Content and conduct of other players

Other players choose their own usernames and play their own games. We do not review
what they do in advance, and we are not responsible for it. Report a problem to the
address in the [Privacy Policy](https://www.tty.cards/privacy/).

---

## 8. Governing law

The service is run by a private individual based in the Netherlands. These terms are
governed by **Dutch law**, and the Dutch courts have jurisdiction, except where mandatory
consumer-protection rules in your country of residence give you the right to bring a
claim locally.

---

## 9. Changes

These terms can change. The date at the top changes with them, and the previous
version stays in the repository's public git history. Continuing to connect after a
change means the new version applies.

---

*This document is a careful, good-faith draft. It is not legal advice. If you operate
this service, have it reviewed by a lawyer in your jurisdiction before relying on it.*
