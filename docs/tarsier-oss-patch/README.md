# TAR-73 + TAR-100 patch (split)

Concatenate parts in order, then:

```bash
cat TAR-73-100.part*.patch.txt > TAR-73-100-combined.patch
cd /path/to/tarsier-dev/tarsier
git checkout -b cursor/tar-73-100-oss-demo-8703
git am < /path/to/TAR-73-100-combined.patch
# or: git apply && git commit
```

Verified `git apply --check` clean on main `002ce3b`.

Also: authorize agent device login (see https://github.com/Pieczasz/tty-cards/issues/18) to let the cloud agent push instead.
