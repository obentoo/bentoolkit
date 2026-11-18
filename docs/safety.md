# Shell Safety Guards

`app-portage/bentoolkit` ships two guards against destructive commands. Both
live in [misc/safety/](../misc/safety/), and the overlay ebuild installs them.

## The shell guard

With `USE=secure` (the default), `/etc/bash/bashrc.d/30-bentoo-safety.bash`
wraps destructive commands in **interactive** bash shells. The wrapped
commands are `rm -r` and `rm` on a critical directory, `dd`, `mkfs`,
`wipefs`, `shred`, the partitioners, `cryptsetup luksFormat`, `chmod -R` and
`chown -R` on a critical directory, `emerge -C`/`--depclean`, `reboot` and
`poweroff`. The same rules apply through `sudo`, `doas`, `run0` and `pkexec`.

A wrapped command waits three seconds, which any key or Ctrl-C cancels, and
then asks for a random four-character code. Some targets are refused outright,
with no code that would let them through: `/`, `~`, `.`, `$HOME` and
`--no-preserve-root`.

Scripts, cron jobs and systemd units are not affected, because they are not
interactive shells. To bypass the guard consciously for one command, run
`BENTOO_NO_GUARD=1 <command>`.

## The Dolphin action

With `USE=kde`, Dolphin's context menu gains *Delete permanently (Bentoo
code)*, which asks for a code in a kdialog before it deletes anything. KDE
deletes through KIO and never calls `rm`, so the shell guard cannot see the
menu's native Delete entry. To protect a path from every deletion, use
`chattr +i <path>`.

## Language

The messages follow `LANGUAGE`, `LC_ALL`, `LC_MESSAGES` and `LANG`, like
`bentoo`. They are read from the same `bentoolkit.mo` catalog, through the
`gettext` command. Two conditions differ from `bentoo`:

- The language's locale must be generated (`locale -a`), because `gettext`
  translates only under an installed locale.
- Without `gettext`, or without a catalog, the messages are English.

The Dolphin menu entry is translated when the package is built
(`make safety`), from the same catalogs.
