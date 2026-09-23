// 英文字典汇总：satisfies 保证与中文字典 key 一一对应，缺漏会在编译期报错
import type { DictKey } from '../zh'
import common from './common'
import layout from './layout'
import login from './login'
import register from './register'
import keys from './keys'
import channels from './channels'
import models from './models'
import tokens from './tokens'
import guide from './guide'
import logs from './logs'
import stats from './stats'
import admin from './admin'
import backup from './backup'
import notify from './notify'

const en = Object.assign({}, common, layout, login, register, keys, channels, models, tokens, guide, logs, stats, admin, backup, notify) satisfies Record<DictKey, string>

export default en
