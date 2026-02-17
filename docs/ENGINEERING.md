# Engineering Principles

Prinsip-prinsip ini membentuk cara berpikir saat menulis kode di Oasis -- baik di level framework maupun application. Ini bukan checklist pattern atau style guide (itu ada di [CONVENTIONS.md](CONVENTIONS.md)). Ini tentang **mental model** yang mendasari setiap keputusan engineering.

## 1. Earn Every Abstraction

Abstraksi itu hutang. Setiap layer, interface, wrapper, atau helper function yang ditambahkan harus **membuktikan nilainya** -- bukan ditambahkan karena "mungkin berguna nanti".

- Tulis kode konkret dulu. Kalau pattern muncul 3x, baru extract.
- Tiga baris kode yang mirip lebih baik daripada satu abstraksi prematur.
- Interface baru harus punya minimal 2 implementasi yang sudah ada atau yang sangat jelas akan ada. Kalau cuma satu implementasi, gunakan concrete type.
- Jangan bikin `utils`, `helpers`, atau `common` package. Kalau sebuah function tidak punya tempat yang jelas, itu tandanya abstraksinya salah.

**Test-nya simpel**: kalau kamu hapus abstraksi itu dan inline kodenya, apakah kode jadi lebih susah dibaca? Kalau tidak, abstraksi itu tidak dibutuhkan.

## 2. Optimize for the Reader

Kode ditulis sekali, dibaca puluhan kali. Setiap keputusan harus memprioritaskan orang yang akan **membaca** kode, bukan yang menulis.

- Nama harus menjelaskan *intent*, bukan *implementation*. `BuildContext` lebih baik dari `GetTop15FactsAndFormat`.
- Komentar menjelaskan **mengapa**, bukan **apa**. Kalau komentar hanya mengulang apa yang sudah jelas dari kode, hapus.
- Flow harus bisa dibaca top-to-bottom. Early return untuk edge cases di atas, happy path di bawah. Jangan nest lebih dari 2 level.
- Satu file, satu concern. Kalau kamu harus scroll lama untuk memahami satu file, file itu terlalu besar.
- Sebuah function yang namanya jelas dan pendek lebih baik dari function yang punya komentar panjang.

## 3. Make It Fast Where It Matters

Performance bukan tentang micro-optimize semua hal. Ini tentang **tahu di mana bottleneck** dan hanya optimize di situ.

**Yang penting di-optimize:**
- **Latency yang user rasakan** -- kalau user menunggu, itu masalah. Stream response daripada buffer. Background-kan pekerjaan yang tidak perlu ditunggu user.
- **External API calls** -- setiap HTTP call itu mahal (100ms+). Batch kalau bisa. Jangan panggil API di loop kalau bisa panggil sekali di luar loop.
- **Memory di hot path** -- kalau sebuah function dipanggil ribuan kali, perhatikan alokasi. Kalau dipanggil sekali saat startup, tidak perlu dipikirkan.

**Yang TIDAK perlu di-optimize:**
- Startup time. Boot lambat 500ms tidak masalah.
- Kode yang jalan sekali per request yang sudah cepat. Jangan optimize 1ms jadi 0.5ms.
- "Bisa lebih efisien kalau pakai X" -- kalau current approach sudah cukup cepat, jangan refactor demi marginal gain.

**Rule of thumb**: kalau kamu tidak bisa mengukur perbedaannya, optimisasi itu tidak penting.

## 4. Fail Gracefully, Recover Automatically

Sistem yang baik bukan yang tidak pernah error -- tapi yang bisa **handle error dengan elegan** dan tetap berfungsi.

- **Never crash on recoverable errors.** Kalau satu subsystem gagal, subsystem lain harus tetap jalan. Memory extraction gagal? Chat tetap jalan tanpa memory. Embedding gagal? Store message tanpa embedding.
- **Distinguish transient vs permanent.** Transient (429, 5xx, timeout) di-retry dengan backoff. Permanent (400, 404, invalid input) langsung return error. Jangan pernah retry permanent errors.
- **Degrade, don't die.** Lebih baik kirim respons tanpa memory context daripada tidak kirim respons sama sekali. Lebih baik kirim plain text daripada crash karena HTML formatting gagal.
- **User harus tahu, tapi tidak perlu tahu detail.** Kalau ada error, informasikan user dengan pesan yang actionable ("Please try again"), bukan stack trace.

## 5. Own Your Dependencies

Setiap dependency yang ditambahkan adalah kode orang lain yang kamu tanggung. Treat dependency addition seperti hire -- harus punya justifikasi yang kuat.

**Pertanyaan sebelum menambahkan dependency:**
1. Apakah masalah ini bisa diselesaikan dengan standard library?
2. Apakah solusi hand-rolled kurang dari 200 baris?
3. Apakah kita butuh lebih dari 30% fitur dari library ini?
4. Apakah library ini actively maintained?
5. Berapa banyak transitive dependencies yang ditarik?

Kalau jawaban 1 atau 2 = ya, jangan tambahkan dependency. Kode sendiri yang 50 baris lebih baik daripada dependency 5000 baris yang kita pakai 1%.

**Khusus untuk external APIs: jangan pakai SDK.** SDK menambahkan coupling yang besar terhadap versi tertentu, sering bloated, dan menyembunyikan apa yang sebenarnya terjadi di wire level. Raw HTTP + JSON memberikan full control dan full visibility. Kalau API berubah, kamu cukup ubah satu file, bukan upgrade major version SDK.

## 6. Design for Replaceability

Setiap komponen harus bisa diganti tanpa merombak sistem. Ini bukan tentang over-engineering -- ini tentang **menaruh seam di tempat yang tepat**.

- **Interface di boundary yang natural.** Tempat yang tepat untuk interface: antara sistem kamu dan external service (LLM, database, messaging platform). Tempat yang salah: antara dua function internal yang selalu berubah bersamaan.
- **Depend on behavior, not implementation.** Consumer seharusnya tidak peduli apakah storage-nya SQLite atau Postgres -- mereka peduli bahwa `SearchChunks` mengembalikan top-K hasil.
- **Configurations, not conditionals.** Kalau sebuah behavior perlu bisa diubah, buat configurable. Jangan hardcode lalu `if/else` nanti.

## 7. Explicit Over Magic

Kode yang jelas dan verbose lebih baik daripada kode yang singkat tapi "ajaib".

- **No hidden side effects.** Kalau sebuah function mengubah state, itu harus terlihat jelas dari nama dan signature-nya. `StoreMessage` jelas menyimpan. `Process` ambigu.
- **Constructor injection, bukan service locator.** Dependencies harus terlihat di function signature, bukan di-resolve diam-diam dari global registry.
- **Prefer parameters over ambient state.** Pass timezone offset sebagai parameter, jangan baca dari global. Pass context explicitly, jangan rely pada goroutine-local storage.
- **Config cascade harus predictable.** defaults -> file -> env vars. Tidak ada "magic override" yang tidak terdokumentasi.

## 8. Ship Incrementally

Lebih baik ship 3 perubahan kecil yang masing-masing benar, daripada 1 perubahan besar yang mungkin salah.

- **Satu PR, satu concern.** Jangan campur refactor dengan feature baru. Jangan campur bug fix dengan "improvement" di area lain.
- **Buat setiap commit bisa di-revert.** Kalau commit kamu di-revert, sistem harus tetap berfungsi.
- **Jangan refactor spekulatif.** "Sekalian aja gue rapiin ini" saat mengerjakan fitur lain = risk tanpa value. Refactor itu task terpisah.
- **Working > perfect.** Ship solusi 80% yang benar hari ini, improve besok. Jangan block feature karena arsitektur belum "ideal".

## 9. Test What Matters

Testing bukan tentang coverage percentage. Ini tentang **confidence** bahwa perubahan tidak merusak hal yang penting.

- **Test behavior, bukan implementation.** Test bahwa chunker menghasilkan output yang benar untuk input tertentu. Jangan test bahwa chunker memanggil `splitOnSentences` secara internal.
- **Pure functions first.** Function tanpa side effect paling mudah dan paling valuable di-test. Prioritaskan ini.
- **Jangan mock kecuali terpaksa.** Kalau kamu perlu mock 5 dependency untuk test satu function, function itu melakukan terlalu banyak hal. Refactor, jangan mock.
- **Edge cases > happy path.** Happy path biasanya obviously benar. Yang menarik: empty input, nil values, concurrent access, boundary conditions.

## 10. Respect the User's Time

Ini bukan cuma tentang end-user -- ini tentang **setiap developer** yang akan berinteraksi dengan kode ini.

- **API yang salah pakai harus susah ditulis.** Kalau consumer bisa lupa langkah penting, buat compiler atau runtime yang mengingatkan -- bukan komentar.
- **Error messages harus actionable.** "invalid args" tidak berguna. "invalid args: expected string for 'query' field" memberi arah.
- **Defaults yang masuk akal.** User yang baru clone repo harus bisa run dengan minimal config. Kalau harus set 20 env vars sebelum bisa jalan, onboarding-nya gagal.
- **Dokumentasi yang hidup.** Docs yang out-of-date lebih berbahaya dari tidak ada docs. Kalau kamu ubah behavior, ubah docs-nya juga. Kalau kamu tambah feature, tulis docs-nya sekarang -- bukan "nanti".
