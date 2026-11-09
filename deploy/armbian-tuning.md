# Armbian 调校（S905M / 1 GB / 16 GB eMMC）

晨曦的两个进程加起来目标 ≤ 72 MiB，但 Armbian 默认装的东西和 swap 策略会先把 1 GB 吃掉。上板前先做这几步。

## 1. 设备树与镜像

S905M 属 Amlogic **GXBB（S905 系列）**，不是 GXL 的 S905X。社区里同芯片（1 GB + 小 eMMC）盒子的可用做法是：用 `amlogic-s9xx-tv-box` 的社区 minimal 镜像，把 `extlinux.conf` / `uEnv.txt` 里的 `fdt` 从 `meson-gxl-s905x-p212.dtb` 改成 `meson-gxbb-p201.dtb`，并把 `u-boot-s905` 重命名为 `u-boot.ext` 才能从 U 盘启动 [citation:6]。不同批次板子网卡/无线需要试几个 DTB，改 `FDT=` 一行即可，不必重刷 [citation:1]。

**先在 U 盘跑通再写 eMMC**：`armbian-ddbr` 先备份原厂安卓分区，确认网络与 USB 正常后再 `armbian-install` [citation:7]。写 eMMC 会清空原数据。

镜像选 **minimal / CLI**，不要带桌面的版本。

## 2. 内存：只用 zram，别把 swap 放 eMMC 或 SD

Armbian 论坛在把四核 A53 限到 1 GB 内存做编译的对照测试里：NVMe 最快（慢 18%），**zram 只慢约 25% 且零成本**，eMMC 明显更慢，SD 卡 swap 直接把 100 分钟的活变成 19 小时；结论是「没有 NVMe 的 SBC 上，压缩内存是最佳选项」 [citation:8]。

```bash
apt install -y zram-tools
printf 'PERCENT=50\nALGO=lz4\n' > /etc/default/zramswap
systemctl enable --now zramswap
```

不要用 `armbian-swap` 在 eMMC 上划 swap 分区：既慢又增加写放大，16 GB eMMC 的寿命比那点swap 值钱。

## 3. 关掉吃内存的服务

```bash
systemctl disable --now bluetooth ModemManager avahi-daemon
systemctl disable --now armbian-zram-config 2>/dev/null || true   # 与 zram-tools 二选一
```

禁用不必要服务、开启 zram（`PERCENT=50`、`ALGO=lz4`）也是 Amlogic 盒子刷 Armbian 的通用优化建议 [citation:11]。

## 4. eMMC 保护

```bash
# /etc/fstab：给根分区加 noatime,commit=60
# 日志交给 journald 并限大小
printf '[Journal]\nStorage=volatile\nRuntimeMaxUse=32M\n' > /etc/systemd/journald.conf.d/10-chenxi.conf
systemctl restart systemd-journald
```

晨曦自己的事件日志有 `max_log_bytes`（默认 256 MiB）上限并会自动 compaction；审计日志按月轮转，别关这个上限。

## 5. 部署后的验收

```bash
systemctl status chenxi chenxi-hostd
free -m
ps -o pid,rss,comm -C chenxi -C chenxi-hostd
curl -s 127.0.0.1:7788/v1/status
```

**别把 7788 端口直接暴露公网**：晨曦沿用「单可信操作者」信任模型，没有多租户隔离。要远程访问就走 SSH 端口转发或 Tailscale。

> 以上内存数字是设计预算，不是实测值。请把 `ps -o rss` 与 `free -m` 的真实读数回写到 `config.json` 的 `budgets`。
